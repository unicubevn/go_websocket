package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for simplicity; adjust for production.
	},
}

// Client represents a WebSocket client connected to the server
type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]bool // Topics this client is subscribed to
}

// Hub manages the topics and clients
type Hub struct {
	clients    map[*Client]bool
	topics     map[string]map[*Client]bool // Topic -> Clients mapping
	register   chan *Client
	unregister chan *Client
	broadcast  chan *Message
	mu         sync.Mutex
}

// Message represents a message to be published to a topic
type Message struct {
	Topic   string
	Content []byte
}

// PublishPayload represents the JSON payload for publishing a message
type PublishPayload struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}

// NewHub creates a new Hub instance
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		topics:     make(map[string]map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan *Message),
	}
}

// Run listens for register, unregister, and broadcast events
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, client)
			for topic := range client.topics {
				delete(h.topics[topic], client)
			}
			close(client.send)
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.Lock()
			if clients, ok := h.topics[message.Topic]; ok {
				for client := range clients {
					select {
					case client.send <- message.Content:
					default:
						close(client.send)
						delete(h.clients, client)
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// HandleMessages handles incoming messages from the client
func (c *Client) HandleMessages(h *Hub) {
	defer func() {
		h.unregister <- c
		c.conn.Close()
	}()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			log.Println("Error reading message:", err)
			break
		}
		topic := string(message)
		h.mu.Lock()
		c.topics[topic] = true
		if _, ok := h.topics[topic]; !ok {
			h.topics[topic] = make(map[*Client]bool)
		}
		h.topics[topic][c] = true
		h.mu.Unlock()

		h.broadcast <- &Message{
			Topic:   topic,
			Content: []byte(fmt.Sprintf("Client subscribed to topic %s", topic)),
		}
	}
}

// WriteMessages sends messages from the hub to the WebSocket client
func (c *Client) WriteMessages() {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Println("Error writing message:", err)
			break
		}
	}
}

// ServeWs handles WebSocket requests from clients
func ServeWs(h *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error upgrading connection:", err)
		return
	}
	client := &Client{conn: conn, send: make(chan []byte, 256), topics: make(map[string]bool)}
	h.register <- client

	go client.HandleMessages(h)
	go client.WriteMessages()
}

// PublishMessage handles the HTTP API request to publish a message to a topic using JSON POST
func PublishMessage(h *Hub, w http.ResponseWriter, r *http.Request) {
	var payload PublishPayload
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	if payload.Topic == "" || payload.Message == "" {
		http.Error(w, "Both 'topic' and 'message' are required", http.StatusBadRequest)
		return
	}

	h.broadcast <- &Message{
		Topic:   payload.Topic,
		Content: []byte(payload.Message),
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("Message sent to topic %s", payload.Topic)))
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	hub := NewHub()
	go hub.Run()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, w, r)
	})

	http.HandleFunc("/publish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
			return
		}
		PublishMessage(hub, w, r)
	})

	fmt.Println("Starting WebSocket server on port " + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
