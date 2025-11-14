package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type User struct {
	ID       int    `json:"id"`
	Email    string `json:"email"`
	Password string `json:"-"`
	UserType string `json:"user_type"`
	Token    string `json:"token"`
}

type Ticket struct {
	ID            int       `json:"id"`
	Email         string    `json:"email"`
	Subject       string    `json:"subject"`
	Description   string    `json:"description"`
	Status        string    `json:"status"`
	AttachmentURL string    `json:"attachment_url,omitempty"`
	ClosedBy      string    `json:"closed_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Message struct {
	ID          int       `json:"id"`
	TicketID    int       `json:"ticket_id"`
	SenderEmail string    `json:"sender_email"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

var db *sql.DB
var s3Client *s3.S3
var activeTokens = make(map[string]User)

func main() {
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(os.Getenv("AWS_REGION")),
	})
	if err != nil {
		log.Printf("Warning: Failed to create AWS session: %v", err)
	} else {
		s3Client = s3.New(sess)
		log.Println("✓ AWS S3 initialized")
	}

	dbHost := os.Getenv("DB_HOST")
	dbUser := os.Getenv("DB_USER")
	dbPass := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_NAME")

	connStr := fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=require",
		dbHost, dbUser, dbPass, dbName)

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Database connection error:", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatal("Database ping error:", err)
	}
	log.Println("✓ Connected to RDS database")

	createTables()
	// Routes
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/login", cors(handleLogin))
	http.HandleFunc("/upload", cors(authenticate(handleUpload)))
	http.HandleFunc("/tickets", cors(authenticate(handleTickets)))
	http.HandleFunc("/tickets/", cors(authenticate(handleTicketActions)))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("✓ Server starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}
// Authentication
func authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		user, exists := activeTokens[token]
		if !exists {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		r.Header.Set("X-User-Email", user.Email)
		r.Header.Set("X-User-Type", user.UserType)

		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// Create database tables
func createTables() {
	// Users table
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			email VARCHAR(255) UNIQUE NOT NULL,
			password VARCHAR(255) NOT NULL,
			user_type VARCHAR(50) NOT NULL
		)
	`)
	if err != nil {
		log.Fatal("Failed to create users table:", err)
	}

	// Insert demo users
	db.Exec(`
		INSERT INTO users (email, password, user_type) 
		VALUES 
			('client@demo.com', 'password123', 'client'),
			('agent@demo.com', 'password123', 'agent')
		ON CONFLICT (email) DO NOTHING
	`)

	// Tickets table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS tickets (
			id SERIAL PRIMARY KEY,
			email VARCHAR(255) NOT NULL,
			subject VARCHAR(200) NOT NULL,
			description TEXT NOT NULL,
			status VARCHAR(50) DEFAULT 'open',
			attachment_url TEXT,
			closed_by VARCHAR(255),
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal("Failed to create tickets table:", err)
	}

	// Messages table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id SERIAL PRIMARY KEY,
			ticket_id INTEGER REFERENCES tickets(id) ON DELETE CASCADE,
			sender_email VARCHAR(255) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal("Failed to create messages table:", err)
	}

	log.Println("✓ Database tables ready")
}

// Login handler
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var user User
	err := db.QueryRow(`
		SELECT id, email, user_type 
		FROM users 
		WHERE email = $1 AND password = $2
	`, creds.Email, creds.Password).Scan(&user.ID, &user.Email, &user.UserType)

	if err != nil {
		log.Printf("Login failed for %s", creds.Email)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Generate token
	user.Token = fmt.Sprintf("%s-%d-%s", user.Email, time.Now().Unix(), uuid.New().String()[:8])
	activeTokens[user.Token] = user

	log.Printf("✓ User logged in: %s (%s)", user.Email, user.UserType)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// Upload file to S3 via VPC endpoint
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userEmail := r.Header.Get("X-User-Email")

	err := r.ParseMultipartForm(5 << 20)
	if err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Generate unique filename
	ext := filepath.Ext(header.Filename)
	filename := fmt.Sprintf("%s-%d-%s%s", userEmail, time.Now().Unix(), uuid.New().String()[:8], ext)

	// Read file content
	fileBytes, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read file", http.StatusInternalServerError)
		return
	}

	// Upload to S3
	bucketName := os.Getenv("S3_BUCKET_NAME")
	_, err = s3Client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("attachments/" + filename),
		Body:   strings.NewReader(string(fileBytes)),
	})

	if err != nil {
		log.Printf("S3 upload error: %v", err)
		http.Error(w, "Failed to upload file", http.StatusInternalServerError)
		return
	}

	// Generate presigned URL
	req, _ := s3Client.GetObjectRequest(&s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String("attachments/" + filename),
	})
	urlStr, err := req.Presign(7 * 24 * time.Hour)
	if err != nil {
		http.Error(w, "Failed to generate URL", http.StatusInternalServerError)
		return
	}

	log.Printf("✓ File uploaded: %s", filename)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": urlStr})
}

// Tickets handler
func handleTickets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		getTickets(w, r)
	case "POST":
		createTicket(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Get tickets
func getTickets(w http.ResponseWriter, r *http.Request) {
	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	var rows *sql.Rows
	var err error

	if userType == "agent" {
		rows, err = db.Query(`
			SELECT id, email, subject, description, status, attachment_url, closed_by, created_at 
			FROM tickets 
			ORDER BY created_at DESC
		`)
	} else {
		rows, err = db.Query(`
			SELECT id, email, subject, description, status, attachment_url, closed_by, created_at 
			FROM tickets 
			WHERE email = $1 
			ORDER BY created_at DESC
		`, userEmail)
	}

	if err != nil {
		log.Printf("Error fetching tickets: %v", err)
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	tickets := []Ticket{}
	for rows.Next() {
		var t Ticket
		var attachmentURL, closedBy sql.NullString
		if err := rows.Scan(&t.ID, &t.Email, &t.Subject, &t.Description, &t.Status, &attachmentURL, &closedBy, &t.CreatedAt); err != nil {
			continue
		}
		if attachmentURL.Valid {
			t.AttachmentURL = attachmentURL.String
		}
		if closedBy.Valid {
			t.ClosedBy = closedBy.String
		}
		tickets = append(tickets, t)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets)
}

// Create ticket
func createTicket(w http.ResponseWriter, r *http.Request) {
	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	if userType != "client" {
		http.Error(w, "Only clients can create tickets", http.StatusForbidden)
		return
	}

	var ticket Ticket
	if err := json.NewDecoder(r.Body).Decode(&ticket); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	ticket.Email = userEmail

	if ticket.Subject == "" || ticket.Description == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	err := db.QueryRow(`
		INSERT INTO tickets (email, subject, description, status, attachment_url) 
		VALUES ($1, $2, $3, 'open', $4) 
		RETURNING id, created_at
	`, ticket.Email, ticket.Subject, ticket.Description, sql.NullString{String: ticket.AttachmentURL, Valid: ticket.AttachmentURL != ""}).Scan(&ticket.ID, &ticket.CreatedAt)

	if err != nil {
		log.Printf("Error creating ticket: %v", err)
		http.Error(w, "Failed to create ticket", http.StatusInternalServerError)
		return
	}

	ticket.Status = "open"
	log.Printf("✓ Ticket #%d created by %s", ticket.ID, ticket.Email)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ticket)
}

// Handle ticket actions
func handleTicketActions(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	ticketID, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "Invalid ticket ID", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && r.Method == "GET" {
		getTicketDetail(w, r, ticketID)
	} else if len(parts) >= 3 {
		action := parts[2]
		switch action {
		case "close":
			closeTicket(w, r, ticketID)
		case "messages":
			handleMessages(w, r, ticketID)
		default:
			http.Error(w, "Invalid action", http.StatusBadRequest)
		}
	} else {
		http.Error(w, "Invalid request", http.StatusBadRequest)
	}
}

// Get single ticket detail
func getTicketDetail(w http.ResponseWriter, r *http.Request, ticketID int) {
	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	var ticket Ticket
	var attachmentURL, closedBy sql.NullString

	query := `SELECT id, email, subject, description, status, attachment_url, closed_by, created_at 
			  FROM tickets WHERE id = $1`
	
	var args []interface{}
	args = append(args, ticketID)

	if userType == "client" {
		query += " AND email = $2"
		args = append(args, userEmail)
	}

	err := db.QueryRow(query, args...).Scan(
		&ticket.ID, &ticket.Email, &ticket.Subject, &ticket.Description,
		&ticket.Status, &attachmentURL, &closedBy, &ticket.CreatedAt,
	)

	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if attachmentURL.Valid {
		ticket.AttachmentURL = attachmentURL.String
	}
	if closedBy.Valid {
		ticket.ClosedBy = closedBy.String
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ticket)
}

// Close ticket
func closeTicket(w http.ResponseWriter, r *http.Request, ticketID int) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	// Check if ticket exists
	var ticketEmail string
	query := "SELECT email FROM tickets WHERE id = $1"
	err := db.QueryRow(query, ticketID).Scan(&ticketEmail)
	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if userType == "client" && ticketEmail != userEmail {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}

	// Close ticket
	_, err = db.Exec("UPDATE tickets SET status = 'closed', closed_by = $1 WHERE id = $2", userEmail, ticketID)
	if err != nil {
		log.Printf("Error closing ticket #%d: %v", ticketID, err)
		http.Error(w, "Failed to close ticket", http.StatusInternalServerError)
		return
	}

	log.Printf("✓ Ticket #%d closed by %s", ticketID, userEmail)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "Ticket closed successfully"})
}

// Handle messages
func handleMessages(w http.ResponseWriter, r *http.Request, ticketID int) {
	switch r.Method {
	case "GET":
		getMessages(w, r, ticketID)
	case "POST":
		createMessage(w, r, ticketID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Get messages for a ticket
func getMessages(w http.ResponseWriter, r *http.Request, ticketID int) {
	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	// Check if user has access to this ticket
	var ticketEmail string
	err := db.QueryRow("SELECT email FROM tickets WHERE id = $1", ticketID).Scan(&ticketEmail)
	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if userType == "client" && ticketEmail != userEmail {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}

	rows, err := db.Query(`
		SELECT id, ticket_id, sender_email, message, created_at 
		FROM messages 
		WHERE ticket_id = $1 
		ORDER BY created_at ASC
	`, ticketID)

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TicketID, &m.SenderEmail, &m.Message, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// Create message (reply)
func createMessage(w http.ResponseWriter, r *http.Request, ticketID int) {
	userEmail := r.Header.Get("X-User-Email")
	userType := r.Header.Get("X-User-Type")

	var ticketEmail string
	err := db.QueryRow("SELECT email FROM tickets WHERE id = $1", ticketID).Scan(&ticketEmail)
	if err != nil {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if userType == "client" && ticketEmail != userEmail {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}

	var msg Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if msg.Message == "" {
		http.Error(w, "Message cannot be empty", http.StatusBadRequest)
		return
	}

	err = db.QueryRow(`
		INSERT INTO messages (ticket_id, sender_email, message) 
		VALUES ($1, $2, $3) 
		RETURNING id, created_at
	`, ticketID, userEmail, msg.Message).Scan(&msg.ID, &msg.CreatedAt)

	if err != nil {
		log.Printf("Error creating message: %v", err)
		http.Error(w, "Failed to send message", http.StatusInternalServerError)
		return
	}

	msg.TicketID = ticketID
	msg.SenderEmail = userEmail

	log.Printf("✓ Message added to ticket #%d by %s", ticketID, userEmail)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg)
}
