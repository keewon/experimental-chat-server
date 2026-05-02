/* ═══════════════════════════════════════════════════════
   CHAT — Multiplexed messenger client
   ═══════════════════════════════════════════════════════ */

// ─── State ──────────────────────────────────────────

const state = {
    me: null,
    rooms: new Map(),         // roomId → {room, my_role, hidden, last_message}
    activeRoomId: null,
    ws: null,
    reconnectAttempts: 0,
    reconnectTimer: null,
    activeTab: 'my',
    publicRooms: [],
    members: [],              // members of activeRoom
    messages: new Map(),      // roomId → array of recent messages (in chronological order)
    lastSeen: loadLastSeen(),
};

const MAX_RECONNECT_ATTEMPTS = 5;

// ─── DOM helpers ────────────────────────────────────

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => document.querySelectorAll(sel);
const el = (tag, props = {}, children = []) => {
    const e = document.createElement(tag);
    for (const k in props) {
        if (k === 'class') e.className = props[k];
        else if (k === 'on') for (const ev in props.on) e.addEventListener(ev, props.on[ev]);
        else if (k === 'data') for (const d in props.data) e.dataset[d] = props.data[d];
        else if (k === 'style') Object.assign(e.style, props.style);
        else if (k === 'text') e.textContent = props[k];
        else if (k === 'html') e.innerHTML = props[k];
        else e[k] = props[k];
    }
    for (const c of children) {
        if (c == null) continue;
        e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    }
    return e;
};
const escapeHtml = (s) => {
    const d = document.createElement('div'); d.textContent = s; return d.innerHTML;
};

// ─── localStorage helpers ───────────────────────────

function loadLastSeen() {
    try { return JSON.parse(localStorage.getItem('chat_last_seen') || '{}'); }
    catch { return {}; }
}
function saveLastSeen() {
    localStorage.setItem('chat_last_seen', JSON.stringify(state.lastSeen));
}
function markSeen(roomId, msgId) {
    if (!msgId) return;
    if ((state.lastSeen[roomId] || 0) < msgId) {
        state.lastSeen[roomId] = msgId;
        saveLastSeen();
    }
}
function unreadCount(roomId, lastMsgId) {
    if (!lastMsgId) return 0;
    return lastMsgId > (state.lastSeen[roomId] || 0) ? 1 : 0; // 1 = "has unread"
}

// ─── Bootstrapping ──────────────────────────────────

async function init() {
    // 1. Ensure session cookie.
    await fetch('/api/session', { credentials: 'same-origin' });

    // 2. Fetch profile.
    const meRes = await fetch('/api/me', { credentials: 'same-origin' });
    if (!meRes.ok) {
        showError('세션 발급 실패');
        return;
    }
    state.me = await meRes.json();

    if (!state.me.display_name) {
        showNicknameModal();
        return;
    }
    showApp();
    connectWS();
}

function showNicknameModal() {
    $('#screen-nickname').style.display = 'flex';
    $('#screen-app').style.display = 'none';
    $('#input-nickname').focus();
}

function showApp() {
    $('#screen-nickname').style.display = 'none';
    $('#screen-app').style.display = 'grid';
    $('#me-name').textContent = state.me.display_name || '--';
    // Mobile boots into the room list (no room selected yet).
    document.querySelector('.app-shell').classList.add('show-list');
}

async function saveNickname(name) {
    name = name.trim();
    if (!name) {
        showNicknameError('닉네임을 입력해주세요.');
        return;
    }
    const res = await fetch('/api/me', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ display_name: name }),
    });
    if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        showNicknameError(data.error || '저장 실패');
        return;
    }
    state.me = await res.json();
    showApp();
    if (!state.ws) connectWS();
}

function showNicknameError(text) {
    const err = $('#nickname-error');
    err.textContent = text; err.style.display = 'block';
}

// ─── WebSocket ──────────────────────────────────────

function connectWS() {
    showStatus('연결 중...', '');
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    state.ws = new WebSocket(`${proto}//${location.host}/ws`);

    state.ws.onopen = () => {
        state.reconnectAttempts = 0;
        showStatus('연결됨', 'connected');
        setTimeout(hideStatus, 1500);
    };
    state.ws.onmessage = (e) => {
        try { handleEvent(JSON.parse(e.data)); }
        catch (err) { console.error('parse', err, e.data); }
    };
    state.ws.onclose = () => {
        state.ws = null;
        if (state.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
            showStatus('연결이 끊어졌어요', 'error');
            $('#btn-reconnect').style.display = 'inline-block';
            return;
        }
        showStatus('재연결 중...', 'error');
        const delay = Math.min(1000 * Math.pow(2, state.reconnectAttempts), 30000);
        state.reconnectAttempts++;
        state.reconnectTimer = setTimeout(connectWS, delay);
    };
}

function manualReconnect() {
    state.reconnectAttempts = 0;
    $('#btn-reconnect').style.display = 'none';
    if (state.ws) try { state.ws.close(); } catch {}
    connectWS();
}

function send(obj) {
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
        state.ws.send(JSON.stringify(obj));
    }
}

function handleEvent(evt) {
    switch (evt.type) {
        case 'snapshot':      onSnapshot(evt); break;
        case 'msg':           onMsg(evt); break;
        case 'error':         onErrorEvent(evt); break;
        case 'member_join':   onMemberChange(evt.room_id); break;
        case 'member_leave':  onMemberChange(evt.room_id); break;
        case 'owner_changed': onOwnerChanged(evt); break;
        case 'room_created':  onRoomCreated(evt); break;
        case 'room_deleted':  onRoomDeleted(evt); break;
        case 'room_updated':  onRoomUpdated(evt); break;
        case 'name_changed':  onNameChanged(evt); break;
    }
}

function onSnapshot(snap) {
    state.me = snap.me;
    $('#me-name').textContent = state.me.display_name;
    state.rooms.clear();
    for (const e of snap.rooms) {
        state.rooms.set(e.room.id, e);
    }
    renderSidebar();
    if (state.activeRoomId && !state.rooms.has(state.activeRoomId)) {
        clearActiveRoom();
    }
}

function onMsg(evt) {
    const r = state.rooms.get(evt.room_id);
    if (r) {
        r.last_message = {
            id: evt.id, room_id: evt.room_id, user_id: evt.user_id,
            display_name: evt.display_name, content: evt.content, created_at: evt.created_at,
        };
    }
    // Append to in-memory log.
    if (!state.messages.has(evt.room_id)) state.messages.set(evt.room_id, []);
    state.messages.get(evt.room_id).push(evt);

    if (state.activeRoomId === evt.room_id) {
        appendChatMessage(evt);
        markSeen(evt.room_id, evt.id);
    }
    renderSidebar();
}

function onErrorEvent(evt) {
    showEmojiError(evt.message || evt.code);
}

function onMemberChange(roomId) {
    if (state.activeRoomId === roomId && $('#members-panel').style.display !== 'none') {
        loadMembers(roomId);
    }
}

function onOwnerChanged(evt) {
    const r = state.rooms.get(evt.room_id);
    if (!r) return;
    r.room.owner_id = evt.new_owner_id;
    r.my_role = state.me.id === evt.new_owner_id ? 'owner' : 'member';
    if (state.activeRoomId === evt.room_id) renderRoomHeader();
    renderSidebar();
}

function onRoomCreated(evt) {
    const r = evt.room;
    if (!state.rooms.has(r.id)) {
        const myRole = r.owner_id === state.me.id ? 'owner' : 'member';
        state.rooms.set(r.id, { room: r, my_role: myRole, hidden: false, last_message: null });
        renderSidebar();
    }
}

function onRoomDeleted(evt) {
    state.rooms.delete(evt.room_id);
    if (state.activeRoomId === evt.room_id) clearActiveRoom();
    renderSidebar();
}

function onRoomUpdated(evt) {
    const r = state.rooms.get(evt.room.id);
    if (r) {
        r.room = evt.room;
        if (state.activeRoomId === evt.room.id) renderRoomHeader();
        renderSidebar();
    }
}

function onNameChanged(evt) {
    if (state.me.id === evt.user_id) {
        state.me.display_name = evt.display_name;
        $('#me-name').textContent = evt.display_name;
    }
    // Existing rendered messages keep the old name (ok for v1).
    if (state.activeRoomId && $('#members-panel').style.display !== 'none') {
        loadMembers(state.activeRoomId);
    }
}

// ─── Rendering ──────────────────────────────────────

function renderSidebar() {
    if (state.activeTab === 'my') renderMyRooms();
    else renderPublicRooms();
}

function renderMyRooms() {
    const list = $('#my-rooms');
    list.innerHTML = '';

    const entries = [...state.rooms.values()]
        .filter(e => !e.hidden)
        .sort((a, b) => {
            if (a.room.kind === 'lobby') return -1;
            if (b.room.kind === 'lobby') return 1;
            const at = a.last_message ? a.last_message.created_at : a.room.created_at;
            const bt = b.last_message ? b.last_message.created_at : b.room.created_at;
            return bt.localeCompare(at);
        });

    for (const e of entries) {
        list.appendChild(roomItemEl(e));
    }
}

function roomItemEl(e) {
    const lastMsgId = e.last_message ? e.last_message.id : 0;
    const unread = unreadCount(e.room.id, lastMsgId);
    const preview = e.last_message
        ? `${e.last_message.display_name || '익명'}: ${e.last_message.content}`
        : (e.room.kind === 'lobby' ? '모두가 함께 있는 방' : '메시지 없음');

    const item = el('li', {
        class: 'room-item' + (state.activeRoomId === e.room.id ? ' active' : ''),
        on: { click: () => selectRoom(e.room.id) },
    }, [
        el('div', { class: 'room-item-content' }, [
            el('div', { class: 'room-item-name', text: e.room.name || '(이름 없음)' }),
            el('div', { class: 'room-item-preview', text: preview }),
        ]),
        unread ? el('span', { class: 'unread-badge', text: '●' }) : null,
    ]);
    return item;
}

async function renderPublicRooms() {
    const list = $('#public-rooms');
    list.innerHTML = '<li style="padding:12px; color:var(--text-muted)">불러오는 중...</li>';
    try {
        const res = await fetch('/api/rooms/public', { credentials: 'same-origin' });
        const data = await res.json();
        state.publicRooms = data.rooms || [];
    } catch (err) {
        list.innerHTML = '<li style="padding:12px; color:var(--danger)">불러오기 실패</li>';
        return;
    }
    list.innerHTML = '';
    if (state.publicRooms.length === 0) {
        list.innerHTML = '<li style="padding:12px; color:var(--text-muted)">공개 채팅방이 없어요</li>';
        return;
    }
    for (const entry of state.publicRooms) {
        const r = entry.room;
        const inIt = state.rooms.has(r.id);
        const item = el('li', { class: 'room-item' }, [
            el('div', { class: 'room-item-content' }, [
                el('div', { class: 'room-item-name', text: r.name }),
                el('div', { class: 'room-item-preview',
                    text: `멤버 ${entry.member_count}명 ${r.emoji_only ? '· 이모지 전용' : ''}` }),
            ]),
            inIt
                ? el('span', { class: 'room-item-kind', text: '참여 중' })
                : el('button', {
                    class: 'btn btn-secondary',
                    text: '참여',
                    on: { click: (ev) => { ev.stopPropagation(); joinPublicRoom(r.id); } }
                }),
        ]);
        list.appendChild(item);
    }
}

async function joinPublicRoom(roomId) {
    const res = await fetch(`/api/rooms/${roomId}/join`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
    });
    if (!res.ok) {
        alert('참여 실패');
        return;
    }
    // member_join via WS will surface the room in sidebar; switch tab.
    setTab('my');
    setTimeout(() => selectRoom(roomId), 200);
}

// ─── Active room ────────────────────────────────────

async function selectRoom(roomId) {
    state.activeRoomId = roomId;
    renderSidebar();

    // Mobile: hide the room list so the chat takes the screen.
    document.querySelector('.app-shell').classList.remove('show-list');

    $('#empty-state').style.display = 'none';
    $('#room-view').style.display = 'flex';
    $('#members-panel').style.display = 'none';

    renderRoomHeader();

    // Load history.
    $('#messages').innerHTML = '';
    try {
        const res = await fetch(`/api/rooms/${roomId}/messages?limit=50`, { credentials: 'same-origin' });
        const data = await res.json();
        const msgs = data.messages || [];
        state.messages.set(roomId, msgs.slice());
        for (const m of msgs) appendChatMessage(m);
        scrollToBottom();
        if (msgs.length > 0) markSeen(roomId, msgs[msgs.length - 1].id);
    } catch (err) {
        console.error(err);
    }
    renderSidebar();
    $('#msg-input').focus();
}

function clearActiveRoom() {
    state.activeRoomId = null;
    $('#room-view').style.display = 'none';
    $('#members-panel').style.display = 'none';
    $('#empty-state').style.display = 'flex';
    document.querySelector('.app-shell').classList.add('show-list');
    renderSidebar();
}

function showRoomList() {
    document.querySelector('.app-shell').classList.add('show-list');
}

function renderRoomHeader() {
    const e = state.rooms.get(state.activeRoomId);
    if (!e) return;
    $('#room-title').textContent = e.room.name;
    let meta = e.room.kind === 'lobby'
        ? '모두 참여'
        : (e.room.visibility === 'public' ? '공개 그룹' : '비공개 그룹');
    if (e.room.emoji_only) meta += ' · 이모지 전용';
    if (e.my_role === 'owner') meta += ' · 방장';
    $('#room-meta').textContent = meta;
}

function appendChatMessage(m) {
    const isSelf = m.user_id === state.me.id;
    const time = new Date(m.created_at).toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
    const div = el('div', { class: 'msg ' + (isSelf ? 'self' : 'other') }, [
        !isSelf ? el('div', { class: 'msg-name', text: m.display_name || '익명' }) : null,
        el('div', { class: 'msg-content', text: m.content }),
        el('div', { class: 'msg-time', text: time }),
    ]);
    $('#messages').appendChild(div);
    scrollToBottom();
}

function scrollToBottom() {
    const m = $('#messages');
    requestAnimationFrame(() => { m.scrollTop = m.scrollHeight; });
}

// ─── Members panel ──────────────────────────────────

async function showMembers() {
    if (!state.activeRoomId) return;
    $('#members-panel').style.display = 'flex';
    await loadMembers(state.activeRoomId);
}

async function loadMembers(roomId) {
    try {
        const res = await fetch(`/api/rooms/${roomId}/members?limit=100`, { credentials: 'same-origin' });
        const data = await res.json();
        state.members = data.members || [];
    } catch { state.members = []; }
    const list = $('#members-list');
    list.innerHTML = '';
    for (const m of state.members) {
        const isMe = m.user_id === state.me.id;
        list.appendChild(el('li', { class: 'member-item' }, [
            el('span', { class: 'member-name', text: (m.display_name || '익명') + (isMe ? ' (나)' : '') }),
            m.role === 'owner' ? el('span', { class: 'member-badge', text: '방장' }) : null,
        ]));
    }
}

// ─── Room menu ──────────────────────────────────────

function showRoomMenu() {
    const e = state.rooms.get(state.activeRoomId);
    if (!e) return;
    const menu = $('#menu-actions');
    menu.innerHTML = '';
    $('#menu-title').textContent = e.room.name;

    if (e.room.kind === 'group') {
        if (e.my_role === 'owner') {
            menu.appendChild(menuBtn('초대 링크 만들기', createInviteLink));
            menu.appendChild(menuBtn('이름 변경', renameRoom));
            menu.appendChild(menuBtn(
                e.room.emoji_only ? '이모지 전용 끄기' : '이모지 전용 켜기',
                toggleEmojiOnly));
            menu.appendChild(menuBtn('방장 양도하고 나가기', openTransferModal, 'danger'));
            menu.appendChild(menuBtn('이 방 삭제 (나 혼자일 때)', leaveRoom, 'danger'));
        } else {
            menu.appendChild(menuBtn('초대 링크 만들기', createInviteLink));
            menu.appendChild(menuBtn('나가기', leaveRoom, 'danger'));
        }
        menu.appendChild(menuBtn(e.hidden ? '숨김 해제' : '사이드바에서 숨기기', toggleHide));
    } else if (e.room.kind === 'lobby') {
        menu.appendChild(el('p', {
            text: '로비는 모두가 참여하는 기본 방입니다.',
            style: { color: 'var(--text-secondary)' }
        }));
    }

    showModal('modal-menu');
}

function menuBtn(text, fn, cls) {
    return el('button', {
        class: cls || '',
        text,
        on: { click: () => { closeModal('modal-menu'); fn(); } }
    });
}

async function createInviteLink() {
    const roomId = state.activeRoomId;
    const res = await fetch(`/api/rooms/${roomId}/invite`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ create_link: true }),
    });
    if (!res.ok) { alert('초대 링크 생성 실패'); return; }
    const data = await res.json();
    const url = `${location.origin}/static/index.html#invite/${data.token}`;
    $('#invite-url').value = url;
    showModal('modal-invite');
}

async function renameRoom() {
    const e = state.rooms.get(state.activeRoomId);
    const name = prompt('새 이름 (1~50자)', e.room.name);
    if (!name) return;
    await fetch(`/api/rooms/${state.activeRoomId}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name }),
    });
}

async function toggleEmojiOnly() {
    const e = state.rooms.get(state.activeRoomId);
    await fetch(`/api/rooms/${state.activeRoomId}`, {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ emoji_only: !e.room.emoji_only }),
    });
}

async function toggleHide() {
    const e = state.rooms.get(state.activeRoomId);
    const path = e.hidden ? 'unhide' : 'hide';
    await fetch(`/api/rooms/${state.activeRoomId}/${path}`, {
        method: 'POST', credentials: 'same-origin'
    });
    e.hidden = !e.hidden;
    if (e.hidden && state.activeRoomId === e.room.id) clearActiveRoom();
    renderSidebar();
}

async function leaveRoom() {
    if (!confirm('이 방에서 나갈까요?')) return;
    const res = await fetch(`/api/rooms/${state.activeRoomId}/leave`, {
        method: 'POST', credentials: 'same-origin'
    });
    if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        if (res.status === 409) {
            alert('방장은 먼저 양도해야 나갈 수 있어요.');
        } else {
            alert(data.error || '나가기 실패');
        }
        return;
    }
    state.rooms.delete(state.activeRoomId);
    clearActiveRoom();
    renderSidebar();
}

async function openTransferModal() {
    const roomId = state.activeRoomId;
    await loadMembers(roomId);
    const select = $('#transfer-target');
    select.innerHTML = '';
    const candidates = state.members.filter(m => m.user_id !== state.me.id);
    if (candidates.length === 0) {
        alert('양도할 수 있는 다른 멤버가 없어요. 그냥 나가면 방이 삭제됩니다.');
        return;
    }
    for (const m of candidates) {
        select.appendChild(el('option', {
            value: m.user_id,
            text: m.display_name || '익명',
        }));
    }
    showModal('modal-transfer');
}

async function doTransfer() {
    const newOwner = $('#transfer-target').value;
    const roomId = state.activeRoomId;
    const res = await fetch(`/api/rooms/${roomId}/transfer`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ new_owner_id: newOwner }),
    });
    if (!res.ok) { alert('양도 실패'); return; }
    closeModal('modal-transfer');
    // After transfer, leave.
    await fetch(`/api/rooms/${roomId}/leave`, { method: 'POST', credentials: 'same-origin' });
}

// ─── Create room ────────────────────────────────────

async function doCreateRoom() {
    const name = $('#create-name').value.trim();
    const visibility = $('#create-visibility').value;
    const emojiOnly = $('#create-emoji-only').checked;
    if (!name) { alert('이름을 입력하세요'); return; }
    const res = await fetch('/api/rooms', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, visibility, emoji_only: emojiOnly }),
    });
    if (!res.ok) { alert('생성 실패'); return; }
    const room = await res.json();
    closeModal('modal-create');
    $('#create-name').value = '';
    setTimeout(() => selectRoom(room.id), 100);
}

// ─── Invite preview (URL hash routing) ──────────────

async function handleHashRoute() {
    const hash = location.hash;
    if (hash.startsWith('#invite/')) {
        const token = hash.slice('#invite/'.length);
        await previewInvite(token);
    }
}

async function previewInvite(token) {
    try {
        const res = await fetch(`/api/invites/${token}`);
        if (!res.ok) { alert('초대 링크가 유효하지 않아요'); return; }
        const data = await res.json();
        if (!confirm(`"${data.room.name}" 방에 참여할까요?\n현재 멤버 ${data.member_count}명`)) return;
        const joinRes = await fetch(`/api/rooms/${data.room.id}/join`, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ invite_token: token }),
        });
        if (!joinRes.ok) { alert('참여 실패'); return; }
        location.hash = '';
        // member_join WS event will land soon; force a fetch select once the room appears.
        setTimeout(() => {
            if (state.rooms.has(data.room.id)) selectRoom(data.room.id);
        }, 300);
    } catch (err) {
        alert('초대 처리 실패');
    }
}

// ─── Modals ─────────────────────────────────────────

function showModal(id) { $('#' + id).style.display = 'flex'; }
function closeModal(id) { $('#' + id).style.display = 'none'; }

// ─── Status indicator ───────────────────────────────

function showStatus(text, cls) {
    const s = $('#connection-status');
    s.style.display = 'flex';
    s.className = 'connection-status ' + (cls || '');
    s.querySelector('.status-text').textContent = text;
    if (cls !== 'error') $('#btn-reconnect').style.display = 'none';
}
function hideStatus() { $('#connection-status').style.display = 'none'; }

function showEmojiError(text) {
    const e = $('#emoji-error');
    e.textContent = text; e.style.display = 'block';
    setTimeout(() => { e.style.display = 'none'; }, 3000);
}
function showError(text) { alert(text); }

// ─── Tabs ───────────────────────────────────────────

function setTab(name) {
    state.activeTab = name;
    $$('.tab-btn').forEach(b => b.classList.toggle('active', b.dataset.tab === name));
    $$('.tab-pane').forEach(p => p.style.display = p.dataset.pane === name ? 'flex' : 'none');
    renderSidebar();
}

// ─── Send message ───────────────────────────────────

function sendCurrentMessage() {
    if (!state.activeRoomId) return;
    const input = $('#msg-input');
    const content = input.value.trim();
    if (!content) return;
    send({ type: 'msg', room_id: state.activeRoomId, content });
    input.value = '';
}

// ─── Wire up ────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    // Nickname
    $('#btn-save-nickname').addEventListener('click', () => saveNickname($('#input-nickname').value));
    $('#input-nickname').addEventListener('keydown', (e) => {
        if (e.key === 'Enter') saveNickname(e.target.value);
    });
    $('#btn-edit-nickname').addEventListener('click', () => {
        const name = prompt('새 닉네임 (1~20자)', state.me.display_name);
        if (name) saveNickname(name);
    });

    // Tabs
    $$('.tab-btn').forEach(b => b.addEventListener('click', () => setTab(b.dataset.tab)));

    // Sidebar
    $('#btn-create-room').addEventListener('click', () => showModal('modal-create'));
    $('#btn-refresh-public').addEventListener('click', renderPublicRooms);

    // Room view
    $('#btn-back-to-list').addEventListener('click', showRoomList);
    $('#btn-show-members').addEventListener('click', showMembers);
    $('#btn-close-members').addEventListener('click', () => $('#members-panel').style.display = 'none');
    $('#btn-room-menu').addEventListener('click', showRoomMenu);
    $('#btn-send').addEventListener('click', sendCurrentMessage);
    $('#msg-input').addEventListener('keydown', (e) => {
        if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendCurrentMessage(); }
    });

    // Modals
    $$('[data-close]').forEach(b => b.addEventListener('click', () => closeModal(b.dataset.close)));
    $('#btn-do-create').addEventListener('click', doCreateRoom);
    $('#btn-do-transfer').addEventListener('click', doTransfer);
    $('#btn-copy-invite').addEventListener('click', () => {
        navigator.clipboard.writeText($('#invite-url').value).then(() => {
            $('#btn-copy-invite').textContent = '복사됨 ✅';
            setTimeout(() => $('#btn-copy-invite').textContent = '복사 📋', 1500);
        });
    });
    $('#btn-reconnect').addEventListener('click', manualReconnect);

    window.addEventListener('hashchange', handleHashRoute);

    init().then(handleHashRoute);
});
