/* ═══════════════════════════════════════════════════════
   EMOJI CHAT — Client Application
   ═══════════════════════════════════════════════════════ */

// ─── State ───────────────────────────────────────────

let userId = null;
let currentRoomId = null;
let ws = null;
let reconnectAttempts = 0;
let reconnectTimer = null;
let oldestMessageId = null;
let publicOrigin = null; // server-supplied origin override (PUBLIC_ORIGIN), filled lazily

// ─── DOM Elements ────────────────────────────────────

const $ = (sel) => document.querySelector(sel);
const screenHome = $('#screen-home');
const screenRoom = $('#screen-room');
const modalQR = $('#modal-qr');
const chatMessages = $('#chat-messages');
const inputMessage = $('#input-message');
const btnSend = $('#btn-send');
const btnCreateRoom = $('#btn-create-room');
const btnJoinRoom = $('#btn-join-room');
const inputRoomId = $('#input-room-id');
const btnBack = $('#btn-back');
const btnQR = $('#btn-qr');
const btnCloseQR = $('#btn-close-qr');
const btnDeleteRoom = $('#btn-delete-room');
const btnCopyUrl = $('#btn-copy-url');
const btnLoadMore = $('#btn-load-more');
const roomNameEl = $('#room-name');
const onlineCountEl = $('#online-count');
const emojiError = $('#emoji-error');
const connectionStatus = $('#connection-status');
const statusText = $('.status-text');

// ─── User ID (server-issued session cookie) ─────────
//
// 서버가 HttpOnly cookie 로 세션을 발급한다. 클라이언트는 자기 user_id 를
// /api/session 으로부터 읽어와 메모리에만 보관 (localStorage 사용 안 함).

async function fetchUserId() {
    const res = await fetch('/api/session', { credentials: 'same-origin' });
    if (!res.ok) throw new Error('session fetch failed: ' + res.status);
    const data = await res.json();
    return data.user_id;
}

// ─── Recent Rooms (localStorage) ─────────────────────

function getRecentRooms() {
    try {
        return JSON.parse(localStorage.getItem('emoji_chat_recent_rooms') || '[]');
    } catch {
        return [];
    }
}

function addRecentRoom(roomId, name) {
    let rooms = getRecentRooms().filter(r => r.id !== roomId);
    rooms.unshift({ id: roomId, name: name || '💬', ts: Date.now() });
    rooms = rooms.slice(0, 10);
    localStorage.setItem('emoji_chat_recent_rooms', JSON.stringify(rooms));
}

function renderRecentRooms() {
    const rooms = getRecentRooms();
    const container = $('#recent-rooms');
    const list = $('#recent-rooms-list');

    if (rooms.length === 0) {
        container.style.display = 'none';
        return;
    }

    container.style.display = 'block';
    list.innerHTML = '';

    rooms.forEach(room => {
        const item = document.createElement('a');
        item.className = 'recent-room-item';
        item.href = `#room/${room.id}`;
        item.innerHTML = `
            <span class="recent-room-emoji">${room.name || '💬'}</span>
            <div class="recent-room-info">
                <div class="recent-room-name">${room.name || '채팅방'}</div>
                <div class="recent-room-id">${room.id}</div>
            </div>
            <span class="recent-room-arrow">→</span>
        `;
        list.appendChild(item);
    });
}

// ─── Emoji Validation ────────────────────────────────

function isEmojiOnly(str) {
    if (!str || str.trim().length === 0) return false;
    const cleaned = str.replace(/\s/g, '');
    if (cleaned.length === 0) return false;
    // Use segmenter if available for accurate grapheme detection
    if (typeof Intl !== 'undefined' && Intl.Segmenter) {
        const segmenter = new Intl.Segmenter('en', { granularity: 'grapheme' });
        const segments = [...segmenter.segment(cleaned)];
        return segments.every(seg => {
            const s = seg.segment;
            const emojiRegex = /^[\p{Emoji_Presentation}\p{Extended_Pictographic}][\u{FE0F}\u{20E3}\u{200D}\p{Emoji_Presentation}\p{Extended_Pictographic}\u{1F3FB}-\u{1F3FF}\u{E0020}-\u{E007F}]*$/u;
            return emojiRegex.test(s);
        });
    }
    // Fallback regex
    const emojiRegex = /^(?:[\p{Emoji_Presentation}\p{Extended_Pictographic}][\u{FE0F}\u{20E3}\u{200D}\p{Emoji_Presentation}\p{Extended_Pictographic}\u{1F3FB}-\u{1F3FF}\u{E0020}-\u{E007F}]*)+$/u;
    return emojiRegex.test(cleaned);
}

// ─── Screen Navigation ──────────────────────────────

function showScreen(screen) {
    screenHome.classList.remove('active');
    screenRoom.classList.remove('active');
    screen.classList.add('active');
}

function showHome() {
    disconnectWS();
    currentRoomId = null;
    showScreen(screenHome);
    renderRecentRooms();
}

async function showRoom(roomId) {
    currentRoomId = roomId;
    oldestMessageId = null;
    showScreen(screenRoom);

    chatMessages.innerHTML = '<div id="load-more-area" class="load-more-area" style="display:none"><button id="btn-load-more" class="btn btn-ghost">이전 메시지 불러오기 ⬆️</button></div>';
    document.getElementById('btn-load-more').addEventListener('click', loadMoreMessages);

    try {
        const res = await fetch(`/api/rooms/${roomId}`);
        if (!res.ok) {
            alert('채팅방을 찾을 수 없습니다 😢');
            location.hash = '#home';
            return;
        }
        const room = await res.json();
        roomNameEl.textContent = room.name || '💬 채팅방';
        addRecentRoom(roomId, room.name);

        // Show delete button if owner
        btnDeleteRoom.style.display = room.owner_id === userId ? 'block' : 'none';
    } catch (err) {
        console.error('Failed to fetch room:', err);
    }

    await loadMessages(roomId);
    connectWS(roomId);
    inputMessage.focus();
}

// ─── API Calls ──────────────────────────────────────

async function createRoom() {
    const name = '💬';
    try {
        const res = await fetch('/api/rooms', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            credentials: 'same-origin',
            body: JSON.stringify({ name }),
        });
        const room = await res.json();
        location.hash = `#room/${room.id}`;
    } catch (err) {
        alert('방 생성에 실패했습니다 😢');
    }
}

async function deleteRoom() {
    if (!confirm('정말 이 채팅방을 삭제하시겠습니까? 🗑️')) return;
    try {
        await fetch(`/api/rooms/${currentRoomId}`, {
            method: 'DELETE',
            credentials: 'same-origin',
        });
        location.hash = '#home';
    } catch (err) {
        alert('방 삭제에 실패했습니다');
    }
}

async function loadMessages(roomId) {
    try {
        const res = await fetch(`/api/rooms/${roomId}/messages?limit=50`);
        const data = await res.json();
        if (data.messages && data.messages.length > 0) {
            data.messages.forEach(msg => appendMessage(msg, false));
            oldestMessageId = data.messages[0].id;
            scrollToBottom();
            if (data.messages.length >= 50) {
                document.getElementById('load-more-area').style.display = 'block';
            }
        }
    } catch (err) {
        console.error('Failed to load messages:', err);
    }
}

async function loadMoreMessages() {
    if (!oldestMessageId || !currentRoomId) return;
    try {
        const res = await fetch(`/api/rooms/${currentRoomId}/messages?limit=50&before=${oldestMessageId}`);
        const data = await res.json();
        if (data.messages && data.messages.length > 0) {
            const loadMoreArea = document.getElementById('load-more-area');
            data.messages.forEach(msg => {
                const el = createMessageElement(msg);
                loadMoreArea.after(el);
            });
            oldestMessageId = data.messages[0].id;
            if (data.messages.length < 50) {
                loadMoreArea.style.display = 'none';
            }
        } else {
            document.getElementById('load-more-area').style.display = 'none';
        }
    } catch (err) {
        console.error('Failed to load more messages:', err);
    }
}

// ─── WebSocket ──────────────────────────────────────

function connectWS(roomId) {
    disconnectWS();

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    // userId is no longer in the query string — the server reads it from the
    // session cookie, which the browser attaches automatically on same-origin WS.
    const url = `${proto}//${location.host}/ws/${roomId}`;

    showConnectionStatus('연결 중...', '');
    ws = new WebSocket(url);

    ws.onopen = () => {
        reconnectAttempts = 0;
        showConnectionStatus('연결됨', 'connected');
        setTimeout(() => hideConnectionStatus(), 2000);
    };

    ws.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            handleWSMessage(msg);
        } catch (err) {
            console.error('WS message parse error:', err);
        }
    };

    ws.onclose = () => {
        ws = null;
        if (currentRoomId === roomId) {
            showConnectionStatus('재연결 중...', 'error');
            scheduleReconnect(roomId);
        }
    };

    ws.onerror = () => {
        // onclose will fire after this
    };
}

function disconnectWS() {
    if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
    }
    if (ws) {
        ws.close();
        ws = null;
    }
    hideConnectionStatus();
}

function scheduleReconnect(roomId) {
    const delay = Math.min(1000 * Math.pow(2, reconnectAttempts), 30000);
    reconnectAttempts++;
    reconnectTimer = setTimeout(() => {
        if (currentRoomId === roomId) {
            connectWS(roomId);
        }
    }, delay);
}

function handleWSMessage(msg) {
    switch (msg.type) {
        case 'message':
            appendMessage(msg, true);
            break;
        case 'join':
            onlineCountEl.textContent = `🟢 ${msg.online_count}`;
            appendSystemMessage(`👋 누군가 들어왔어요`);
            break;
        case 'leave':
            onlineCountEl.textContent = `🟢 ${msg.online_count}`;
            appendSystemMessage(`🚶 누군가 나갔어요`);
            break;
        case 'error':
            showEmojiError(msg.message);
            break;
    }
}

function sendMessage() {
    const content = inputMessage.value.trim();
    if (!content) return;

    if (!isEmojiOnly(content)) {
        showEmojiError('이모지만 입력할 수 있어요! 🙅');
        return;
    }

    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'message', content }));
        inputMessage.value = '';
        hideEmojiError();
    }
}

// ─── Message Rendering ──────────────────────────────

function createMessageElement(msg) {
    const isSelf = msg.user_id === userId;
    const div = document.createElement('div');
    div.className = `msg-bubble ${isSelf ? 'self' : 'other'}`;

    const time = new Date(msg.created_at).toLocaleTimeString('ko-KR', {
        hour: '2-digit',
        minute: '2-digit',
    });

    const shortId = msg.user_id.substring(0, 6);

    div.innerHTML = `
        <div class="msg-content">${escapeHtml(msg.content)}</div>
        <div class="msg-meta">${isSelf ? '' : shortId + ' · '}${time}</div>
    `;
    return div;
}

function appendMessage(msg, animate) {
    const el = createMessageElement(msg);
    if (!animate) el.style.animation = 'none';
    chatMessages.appendChild(el);

    if (msg.id && (!oldestMessageId || msg.id < oldestMessageId)) {
        oldestMessageId = msg.id;
    }

    // Auto-scroll if near bottom
    const threshold = 150;
    const isNearBottom = chatMessages.scrollHeight - chatMessages.scrollTop - chatMessages.clientHeight < threshold;
    if (isNearBottom || (msg.user_id === userId)) {
        scrollToBottom();
    }
}

function appendSystemMessage(text) {
    const div = document.createElement('div');
    div.className = 'msg-system';
    div.textContent = text;
    chatMessages.appendChild(div);
    scrollToBottom();
}

function scrollToBottom() {
    requestAnimationFrame(() => {
        chatMessages.scrollTop = chatMessages.scrollHeight;
    });
}

function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// ─── QR Code ─────────────────────────────────────────

async function getPublicOrigin() {
    if (publicOrigin !== null) return publicOrigin;
    try {
        const res = await fetch('/api/config');
        const cfg = await res.json();
        publicOrigin = (cfg && cfg.public_origin) ? cfg.public_origin : location.origin;
    } catch {
        publicOrigin = location.origin;
    }
    return publicOrigin;
}

async function showQRModal() {
    const origin = await getPublicOrigin();
    const url = `${origin}/static/index.html#room/${currentRoomId}`;
    const qrContainer = $('#qr-code');
    qrContainer.innerHTML = '';

    new QRCode(qrContainer, {
        text: url,
        width: 200,
        height: 200,
        colorDark: '#1a1a3e',
        colorLight: '#ffffff',
        correctLevel: QRCode.CorrectLevel.M,
    });

    $('#qr-url-display').value = url;
    modalQR.style.display = 'flex';
}

function hideQRModal() {
    modalQR.style.display = 'none';
}

function copyRoomUrl() {
    const url = $('#qr-url-display').value;
    navigator.clipboard.writeText(url).then(() => {
        btnCopyUrl.textContent = '복사됨! ✅';
        setTimeout(() => { btnCopyUrl.textContent = '복사 📋'; }, 2000);
    });
}

// ─── Connection Status UI ────────────────────────────

function showConnectionStatus(text, cls) {
    connectionStatus.style.display = 'flex';
    connectionStatus.className = 'connection-status ' + (cls || '');
    statusText.textContent = text;
}

function hideConnectionStatus() {
    connectionStatus.style.display = 'none';
}

// ─── Emoji Error UI ──────────────────────────────────

function showEmojiError(text) {
    emojiError.textContent = text;
    emojiError.style.display = 'block';
    setTimeout(hideEmojiError, 3000);
}

function hideEmojiError() {
    emojiError.style.display = 'none';
}

// ─── Routing ─────────────────────────────────────────

function route() {
    const hash = location.hash;
    if (hash.startsWith('#room/')) {
        const roomId = hash.slice(6);
        if (roomId) {
            showRoom(roomId);
            return;
        }
    }
    showHome();
}

// ─── Event Listeners ─────────────────────────────────

btnCreateRoom.addEventListener('click', createRoom);

btnJoinRoom.addEventListener('click', () => {
    let id = inputRoomId.value.trim();
    // Extract room ID from URL if pasted
    const match = id.match(/#room\/([a-f0-9-]+)/i) || id.match(/([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})/i);
    if (match) id = match[1];
    if (id) {
        location.hash = `#room/${id}`;
    }
});

inputRoomId.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') btnJoinRoom.click();
});

btnBack.addEventListener('click', () => {
    location.hash = '#home';
});

btnQR.addEventListener('click', showQRModal);
btnCloseQR.addEventListener('click', hideQRModal);
$('.modal-backdrop').addEventListener('click', hideQRModal);

btnDeleteRoom.addEventListener('click', deleteRoom);
btnCopyUrl.addEventListener('click', copyRoomUrl);

btnSend.addEventListener('click', sendMessage);

inputMessage.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        sendMessage();
    }
});

window.addEventListener('hashchange', route);

// ─── Init ────────────────────────────────────────────

(async () => {
    try {
        userId = await fetchUserId();
    } catch (err) {
        console.error(err);
        alert('세션 발급에 실패했습니다 😢');
        return;
    }
    route();
})();
