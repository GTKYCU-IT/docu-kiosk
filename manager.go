package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type Manager struct {
	clients ClientList
	sync.RWMutex

	otps *RetentionMap

	handlers map[EventType]EventHandler
}

func NewManager(ctx context.Context) *Manager {
	m := &Manager{
		clients:  make(ClientList),
		handlers: make(map[EventType]EventHandler),
		otps:     NewRetentionMap(ctx, 5*time.Second),
	}
	m.setupEventHandlers()
	return m
}

func (m *Manager) setupEventHandlers() {
	m.handlers[EventSendMessage] = SendMessage
	m.handlers[EventChangeRoom] = ChangeRoom
}

func ChangeRoom(event Event, c *Client) error {
	var changeRoomEvent ChangeRoomEvent
	if err := json.Unmarshal(event.Payload, &changeRoomEvent); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}

	c.chatroom = changeRoomEvent.Name

	return nil
}

func SendMessage(event Event, c *Client) error {
	var chatEvent SendMessageEvent
	if err := json.Unmarshal(event.Payload, &chatEvent); err != nil {
		return fmt.Errorf("bad payload in request: %w", err)
	}

	var broadcastMessage NewMessageEvent
	broadcastMessage.Sent = time.Now()
	broadcastMessage.Message = chatEvent.Message
	broadcastMessage.From = chatEvent.From

	data, err := json.Marshal(broadcastMessage)
	if err != nil {
		return fmt.Errorf("failed ot marshal broascast message: %w", err)
	}

	outgoingEvent := Event{
		Payload: data,
		Type:    EventNewMessage,
	}

	c.manager.RLock()
	clients := make(ClientList)
	for client := range c.manager.clients {
		clients[client] = true
	}
	c.manager.RUnlock()

	for client := range clients {
		if client.chatroom == c.chatroom {
			client.egress <- outgoingEvent
		}
	}

	return nil
}

func (m *Manager) routeEvent(event Event, c *Client) error {
	if handler, ok := m.handlers[event.Type]; ok {
		if err := handler(event, c); err != nil {
			return err
		}
		return nil
	} else {
		return errors.New("there is no such event type")
	}
}

func (m *Manager) serveWS(w http.ResponseWriter, r *http.Request) {
	otp, err := uuid.Parse(r.URL.Query().Get("otp"))
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if !m.otps.VerifyOTP(otp) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	log.Println("new connetcion")

	// upgrade
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := NewClient(conn, m)
	m.addClient(client)

	// start client procs
	go client.readMessages()
	go client.writeMessages()
}

func (m *Manager) registrationHandler(w http.ResponseWriter, r *http.Request) {
}

func (m *Manager) loginHandler(w http.ResponseWriter, r *http.Request) {
	type userLoginRequest struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	decoder := json.NewDecoder(r.Body)
	defer r.Body.Close()

	var req userLoginRequest
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Println(req)

	if req.Username == "percy" && req.Password == "123" {
		type response struct {
			OTP uuid.UUID `json:"otp"`
		}

		otp := m.otps.NewOTP()

		res := response{OTP: otp.Key}
		data, err := json.Marshal(res)
		if err != nil {
			log.Println(err)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(data)
		return
	}

	w.WriteHeader(http.StatusUnauthorized)
}

func (m *Manager) addClient(client *Client) {
	m.Lock()
	defer m.Unlock()

	m.clients[client] = true
}

func (m *Manager) removeClient(client *Client) {
	m.Lock()
	defer m.Unlock()

	if _, ok := m.clients[client]; ok {
		client.connection.CloseNow()
		delete(m.clients, client)
	}
}
