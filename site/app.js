
const API_BASE = 'http://alb-dns';

const $ = sel => document.querySelector(sel);

let currentUser = null;
let currentTicketId = null;

const loginScreen = $('#login-screen');
const appScreen = $('#app-screen');
const loginForm = $('#login-form');
const loginMsg = $('#login-msg');
const ticketForm = $('#ticket-form');
const formMsg = $('#form-msg');
const ticketsList = $('#tickets-list');
const submitSection = $('#submit-section');
const userInfo = $('#user-info');
const ticketsTitle = $('#tickets-title');
const ticketModal = $('#ticket-modal');
const modalClose = $('#modal-close');
const closeTicketBtn = $('#close-ticket-btn');
const replyForm = $('#reply-form');

const savedUser = sessionStorage.getItem('user');
if (savedUser) {
  currentUser = JSON.parse(savedUser);
  showApp();
}

loginForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const email = $('#login-email').value.trim();
  const password = $('#login-password').value.trim();

  try {
    const res = await fetch(`${API_BASE}/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password })
    });

    if (!res.ok) throw new Error('Invalid credentials');
    
    const data = await res.json();
    currentUser = data;
    sessionStorage.setItem('user', JSON.stringify(data));
    
    showApp();
  } catch (err) {
    showLoginMsg('Login failed: ' + err.message, true);
  }
});

$('#logout-btn').addEventListener('click', () => {
  currentUser = null;
  sessionStorage.removeItem('user');
  loginScreen.style.display = 'flex';
  appScreen.style.display = 'none';
  loginForm.reset();
});

function showApp() {
  loginScreen.style.display = 'none';
  appScreen.style.display = 'block';
  
  userInfo.textContent = `Logged in as ${currentUser.user_type}: ${currentUser.email}`;
  
  if (currentUser.user_type === 'agent') {
    submitSection.style.display = 'none';
    ticketsTitle.textContent = 'All Tickets';
  } else {
    submitSection.style.display = 'block';
    ticketsTitle.textContent = 'Your Tickets';
  }
  
  loadTickets();
}

ticketForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  
  const subject = $('#subject').value.trim();
  const description = $('#description').value.trim();
  const attachmentFile = $('#attachment').files[0];
  
  if (!subject || !description) {
    showFormMsg('Please fill all fields', true);
    return;
  }

  if (attachmentFile && attachmentFile.size > 5 * 1024 * 1024) {
    showFormMsg('File too large. Max 5MB', true);
    return;
  }
  
  showFormMsg('Submitting...');
  
  try {
    let attachmentUrl = null;
    
    if (attachmentFile) {
      const formData = new FormData();
      formData.append('file', attachmentFile);
      
      const uploadRes = await fetch(`${API_BASE}/upload`, {
        method: 'POST',
        headers: { 'Authorization': currentUser.token },
        body: formData
      });
      
      if (!uploadRes.ok) throw new Error('Failed to upload attachment');
      const uploadData = await uploadRes.json();
      attachmentUrl = uploadData.url;
    }

    const res = await fetch(`${API_BASE}/tickets`, {
      method: 'POST',
      headers: { 
        'Content-Type': 'application/json',
        'Authorization': currentUser.token
      },
      body: JSON.stringify({
        subject,
        description,
        attachment_url: attachmentUrl
      })
    });
    
    if (!res.ok) throw new Error(await res.text());
    
    const data = await res.json();
    showFormMsg('Ticket created! #' + data.id, false);
    ticketForm.reset();
    loadTickets();
  } catch (err) {
    showFormMsg('Error: ' + err.message, true);
  }
});

async function loadTickets() {
  ticketsList.innerHTML = 'Loading...';
  
  try {
    const res = await fetch(`${API_BASE}/tickets`, {
      headers: { 'Authorization': currentUser.token }
    });
    
    if (!res.ok) throw new Error('Failed to load tickets');
    
    const tickets = await res.json();
    
    if (!tickets || tickets.length === 0) {
      ticketsList.innerHTML = '<p style="color:#6b7280">No tickets found.</p>';
      return;
    }
    
    ticketsList.innerHTML = '';
    tickets.reverse().forEach(ticket => {
      const div = document.createElement('div');
      div.className = 'ticket';
      div.onclick = () => openTicketModal(ticket.id);
      
      const statusClass = ticket.status === 'open' ? 'status-open' : 'status-closed';
      
      div.innerHTML = `
        <div class="ticket-header">
          <strong>#${ticket.id} — ${escape(ticket.subject)}</strong>
          <span class="ticket-status ${statusClass}">${ticket.status}</span>
        </div>
        <div class="ticket-meta">
          ${escape(ticket.email)} • ${new Date(ticket.created_at).toLocaleString()}
          ${ticket.closed_by ? `<br>Closed by: ${escape(ticket.closed_by)}` : ''}
        </div>
        <p>${escape(ticket.description.substring(0, 100))}${ticket.description.length > 100 ? '...' : ''}</p>
      `;
      
      ticketsList.appendChild(div);
    });
  } catch (err) {
    ticketsList.innerHTML = `<p style="color:#dc2626">Error: ${err.message}</p>`;
  }
}

async function openTicketModal(ticketId) {
  currentTicketId = ticketId;
  
  try {

    const res = await fetch(`${API_BASE}/tickets/${ticketId}`, {
      headers: { 'Authorization': currentUser.token }
    });
    
    if (!res.ok) throw new Error('Failed to load ticket');
    const ticket = await res.json();
    
    $('#modal-title').textContent = `Ticket #${ticket.id}`;
    $('#ticket-details').innerHTML = `
      <div class="detail-row">
        <div class="detail-label">Subject</div>
        <div class="detail-value"><strong>${escape(ticket.subject)}</strong></div>
      </div>
      <div class="detail-row">
        <div class="detail-label">Status</div>
        <div class="detail-value">
          <span class="ticket-status ${ticket.status === 'open' ? 'status-open' : 'status-closed'}">
            ${ticket.status}
          </span>
        </div>
      </div>
      <div class="detail-row">
        <div class="detail-label">Created By</div>
        <div class="detail-value">${escape(ticket.email)}</div>
      </div>
      <div class="detail-row">
        <div class="detail-label">Created At</div>
        <div class="detail-value">${new Date(ticket.created_at).toLocaleString()}</div>
      </div>
      ${ticket.closed_by ? `
        <div class="detail-row">
          <div class="detail-label">Closed By</div>
          <div class="detail-value">${escape(ticket.closed_by)}</div>
        </div>
      ` : ''}
      <div class="detail-row">
        <div class="detail-label">Description</div>
        <div class="detail-value">${escape(ticket.description)}</div>
      </div>
      ${ticket.attachment_url ? `
        <div class="detail-row">
          <div class="detail-label">Attachment</div>
          <div class="detail-value">
            <a href="${ticket.attachment_url}" target="_blank" class="attachment-link">
              📎 View Attachment
            </a>
          </div>
        </div>
      ` : ''}
    `;
    
    loadMessages(ticketId);
    
    if (ticket.status === 'closed') {
      closeTicketBtn.style.display = 'none';
      replyForm.style.display = 'none';
    } else {
      closeTicketBtn.style.display = 'block';
      replyForm.style.display = 'block';
    }
    
    ticketModal.style.display = 'flex';
  } catch (err) {
    alert('Error loading ticket: ' + err.message);
  }
}

async function loadMessages(ticketId) {
  try {
    const res = await fetch(`${API_BASE}/tickets/${ticketId}/messages`, {
      headers: { 'Authorization': currentUser.token }
    });
    
    if (!res.ok) throw new Error('Failed to load messages');
    const messages = await res.json();
    
    const messagesList = $('#messages-list');
    
    if (!messages || messages.length === 0) {
      messagesList.innerHTML = '<p style="color:#6b7280;text-align:center">No messages yet.</p>';
      return;
    }
    
    messagesList.innerHTML = '';
    messages.forEach(msg => {
      const div = document.createElement('div');
      div.className = 'message';
      div.innerHTML = `
        <div class="message-header">
          <span class="message-sender">${escape(msg.sender_email)}</span>
          <span class="message-time">${new Date(msg.created_at).toLocaleString()}</span>
        </div>
        <div class="message-text">${escape(msg.message)}</div>
      `;
      messagesList.appendChild(div);
    });
    
    messagesList.scrollTop = messagesList.scrollHeight;
  } catch (err) {
    $('#messages-list').innerHTML = `<p style="color:#dc2626">Error loading messages</p>`;
  }
}

replyForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  
  const message = $('#reply-message').value.trim();
  if (!message) return;
  
  try {
    const res = await fetch(`${API_BASE}/tickets/${currentTicketId}/messages`, {
      method: 'POST',
      headers: { 
        'Content-Type': 'application/json',
        'Authorization': currentUser.token
      },
      body: JSON.stringify({ message })
    });
    
    if (!res.ok) throw new Error('Failed to send message');
    
    $('#reply-message').value = '';
    loadMessages(currentTicketId);
  } catch (err) {
    alert('Error: ' + err.message);
  }
});

closeTicketBtn.addEventListener('click', async () => {
  if (!confirm('Are you sure you want to close this ticket?')) return;
  
  try {
    const res = await fetch(`${API_BASE}/tickets/${currentTicketId}/close`, {
      method: 'POST',
      headers: { 'Authorization': currentUser.token }
    });
    
    if (!res.ok) throw new Error('Failed to close ticket');
    
    alert('Ticket closed successfully');
    ticketModal.style.display = 'none';
    loadTickets();
  } catch (err) {
    alert('Error: ' + err.message);
  }
});

modalClose.addEventListener('click', () => {
  ticketModal.style.display = 'none';
});

ticketModal.addEventListener('click', (e) => {
  if (e.target === ticketModal) {
    ticketModal.style.display = 'none';
  }
});

$('#refresh').addEventListener('click', loadTickets);

function showLoginMsg(text, isError) {
  loginMsg.textContent = text;
  loginMsg.style.background = isError ? '#fee2e2' : '#d1fae5';
  loginMsg.style.color = isError ? '#dc2626' : '#065f46';
}

function showFormMsg(text, isError) {
  formMsg.textContent = text;
  formMsg.style.background = isError ? '#fee2e2' : '#d1fae5';
  formMsg.style.color = isError ? '#dc2626' : '#065f46';
}

function escape(str) {
  if (!str) return '';
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}
