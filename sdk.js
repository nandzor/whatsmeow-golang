class ChatSDK {
    constructor() {
        this.unreadMessagesCount = 0;
        this.isChatOpen = false;
        this.currentUser = '6285123945816@s.whatsapp.net'; // Example user ID, implement dynamic logic
        this.ws = new WebSocket('ws://100.81.120.54:8050/ws');
        this.messages = [];

        this.ws.onmessage = (event) => this.onMessage(event);

        this.createChatInterface();
    }

    createChatInterface() {
        // Create chat balloon
        const chatBalloon = document.createElement('div');
        chatBalloon.className = 'chat-balloon';

        // Create badge
        const badge = document.createElement('div');
        badge.id = 'messageBadge';
        badge.className = 'badge';
        badge.textContent = '0';
        chatBalloon.appendChild(badge);

        // Create chat button
        const chatButton = document.createElement('div');
        chatButton.id = 'chatButton';
        chatButton.className = 'chat-button';
        chatButton.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" class="h-8 w-8 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 10h.01M12 10h.01M16 10h.01M9 16H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-5l-5 5v-5z"/></svg>';
        chatBalloon.appendChild(chatButton);

        // Create chat window
        const chatWindow = document.createElement('div');
        chatWindow.id = 'chatWindow';
        chatWindow.className = 'chat-window';

        // Chat window header
        const header = document.createElement('div');
        header.className = 'bg-gradient-to-r from-purple-500 to-pink-500 text-white py-4 px-6 flex items-center justify-between';
        header.innerHTML = `<h1 class="text-lg font-bold">Instagram Chat</h1><button id="closeChat" class="text-sm font-medium hover:text-gray-200">Close</button>`;
        chatWindow.appendChild(header);

        // Messages list
        const messagesList = document.createElement('ul');
        messagesList.id = 'messages';
        messagesList.className = 'scrollbar flex-grow overflow-y-auto p-4 space-y-2';
        chatWindow.appendChild(messagesList);

        // Chat input area
        const inputArea = document.createElement('div');
        inputArea.className = 'border-t border-gray-200 p-4';
        inputArea.innerHTML = `<div class="flex items-center space-x-2"><input type="text" id="messageInput" placeholder="Message..." class="flex-grow px-4 py-2 border border-gray-300 rounded-full focus:outline-none focus:border-purple-500" /><button id="sendButton" class="px-4 py-2 bg-gradient-to-r from-purple-500 to-pink-500 text-white rounded-full hover:opacity-90 transition duration-200">Send</button></div>`;
        chatWindow.appendChild(inputArea);

        chatBalloon.appendChild(chatWindow);
        document.body.appendChild(chatBalloon);

        // Add event listeners after DOM elements are created
        chatButton.addEventListener('click', () => this.toggleChat());
        document.getElementById('closeChat').addEventListener('click', () => this.toggleChat());
        document.getElementById('sendButton').addEventListener('click', () => this.sendMessage(document.getElementById('messageInput').value));
        document.getElementById('messageInput').addEventListener('keypress', (event) => {
            if (event.key === 'Enter') this.sendMessage(document.getElementById('messageInput').value);
        });

        // Add audio element for notifications
        const audio = document.createElement('audio');
        audio.id = 'notificationSound';
        audio.src = 'http://8.219.234.252/beep.mp3';
        audio.preload = 'auto';
        document.body.appendChild(audio);
    }

    // ... (rest of the methods from the previous version)

    formatTimestamp(givenTimestamp) {
        const parseTimestamp = (timestamp) => 
            new Date(typeof timestamp === 'string' ? timestamp.replace(/\s+[A-Z]{3,4}(\s|$)/, '') : timestamp);
        
        const givenDate = parseTimestamp(givenTimestamp);
        
        if (isNaN(givenDate.getTime())) {
            throw new Error('Invalid timestamp format');
        }
        
        const now = new Date();
        const oneDay = 24 * 60 * 60 * 1000;
        
        // Normalize both dates to the start of the day (midnight)
        const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
        const startOfGivenDate = new Date(givenDate.getFullYear(), givenDate.getMonth(), givenDate.getDate());
        
        const timeDiff = now - givenDate;
        
        const formatTime = (date) => {
            const hours = date.getHours();
            return `${(hours % 12 || 12)}:${date.getMinutes().toString().padStart(2, '0')} ${hours < 12 ? 'AM' : 'PM'}`;
        };
        
        // Check if the given date is today
        if (startOfGivenDate.getTime() === startOfToday.getTime()) {
            return `Today at ${formatTime(givenDate)}`;
        }
        
        // Check if the given date is yesterday
        const startOfYesterday = new Date(startOfToday.getTime() - oneDay);
        if (startOfGivenDate.getTime() === startOfYesterday.getTime()) {
            return `Yesterday at ${formatTime(givenDate)}`;
        }
        
        // Otherwise, return the full date and time
        return `${givenDate.toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric', weekday: 'long' })}, at ${formatTime(givenDate)}`;
    }

    updateBadge() {
        const badge = document.getElementById('messageBadge');
        if (this.unreadMessagesCount > 0 && !this.isChatOpen) {
            badge.textContent = this.unreadMessagesCount;
            badge.style.display = 'block';
        } else {
            badge.style.display = 'none';
        }
    }

    playNotificationSound() {
        const sound = document.getElementById('notificationSound');
			sound.currentTime = 0; // Reset sound to start

			// Use AudioContext to bypass autoplay restrictions
			const audioContext = new (window.AudioContext || window.webkitAudioContext)();
			fetch(sound.src)
				.then(response => response.arrayBuffer())
				.then(arrayBuffer => audioContext.decodeAudioData(arrayBuffer))
				.then(audioBuffer => {
					const source = audioContext.createBufferSource();
					source.buffer = audioBuffer;
					source.connect(audioContext.destination);
					source.start(0); // Play immediately
				})
				.catch(error => console.error('Error loading audio:', error));
    }

    vibrateChatBalloon() {
        const chatButton = document.getElementById('chatButton');
        chatButton.classList.add('vibrate');
        setTimeout(() => {
            chatButton.classList.remove('vibrate');
        }, 500);
    }

    onMessage(event) {
        const message = JSON.parse(event.data);
        if (message.sender !== this.currentUser && !this.isChatOpen) {
            this.unreadMessagesCount++;
            this.updateBadge();
            this.playNotificationSound();
            this.vibrateChatBalloon();
        }
        this.addMessageToChat(message);
    }

    addMessageToChat(message) {
        const messagesList = document.getElementById('messages');
        const li = document.createElement('li');
        const isOutbound = message.sender === this.currentUser;
        
        li.className = `flex ${isOutbound ? 'justify-end' : 'justify-start'} mb-2`;
        li.innerHTML = `
            <div class="max-w-xs lg:max-w-md ${isOutbound ? 'bg-gradient-to-r from-purple-500 to-pink-500 text-white' : 'bg-gray-200'} rounded-2xl px-4 py-2 ${isOutbound ? 'rounded-br-none' : 'rounded-bl-none'}">
                <p class="text-sm">${message.message}</p>
                <p class="text-xs text-${isOutbound ? 'gray-200' : 'gray-500'} mt-1">${this.formatTimestamp(message.timestamp)}</p>
            </div>
        `;
        messagesList.appendChild(li);
        messagesList.scrollTop = messagesList.scrollHeight;
    }

    async fetchMessages() {
        try {
            const response = await fetch('http://100.81.120.54:8050/receive-message');
            if (!response.ok) throw new Error(`HTTP error! Status: ${response.status}`);
            const data = await response.json();
            this.messages = data.received_messages;
            this.updateChatUI();
        } catch (error) {
            console.error('Error fetching messages:', error);
        }
    }

    updateChatUI() {
        const messagesList = document.getElementById('messages');
        messagesList.innerHTML = '';
        this.messages.forEach(message => this.addMessageToChat(message));
        messagesList.scrollTop = messagesList.scrollHeight;
    }

    async sendMessage(messageText) {
        if (messageText) {
            const payload = {
                recipient: '6282354777001', // Example recipient, implement dynamic logic
                message: messageText,
            };
            try {
                const response = await fetch('http://100.81.120.54:8050/send-message', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload),
                });
                if (!response.ok) throw new Error(`HTTP error! Status: ${response.status}`);
                this.addMessageToChat({
                    sender: this.currentUser,
                    message: messageText,
                    timestamp: new Date().toISOString()
                });
            } catch (error) {
                console.error('Error sending message:', error);
            }
        }
        document.getElementById('messageInput').value = ''; // Clear input after sending
    }

    toggleChat() {
        const chatWindow = document.getElementById('chatWindow');
        if (chatWindow.style.display === 'none' || chatWindow.style.display === '') {
            chatWindow.style.display = 'flex';
            this.isChatOpen = true;
            this.unreadMessagesCount = 0;
            this.updateBadge();
            this.fetchMessages();
        } else {
            chatWindow.style.display = 'none';
            this.isChatOpen = false;
        }
    }
}

export default ChatSDK;