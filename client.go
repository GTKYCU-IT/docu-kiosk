package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/coder/websocket"
)

var (
	pongWait = 10 * time.Second

	pingInterval = (pongWait * 9) / 10
)

type ClientList map[*Client]bool

type Client struct {
	connection *websocket.Conn
	manager    *Manager

	chatroom string

	// avoid concurrent writes
	egress chan Event
}

func NewClient(conn *websocket.Conn, manager *Manager) *Client {
	return &Client{
		connection: conn,
		manager:    manager,
		egress:     make(chan Event, 256),
	}
}

func (c *Client) readMessages() {
	defer func() {
		// cleanup conn
		c.manager.removeClient(c)
	}()

	c.connection.SetReadLimit(512)

	for {
		_, payload, err := c.connection.Read(context.Background())
		if err != nil {
			switch websocket.CloseStatus(err) {
			case websocket.StatusGoingAway:
				fallthrough
			case websocket.StatusAbnormalClosure:
				// only log unexpected closures
				log.Println(err)
			}
			return
		}

		var request Event
		if err := json.Unmarshal(payload, &request); err != nil {
			log.Println("error marshalling event", err)
			continue
		}

		if err := c.manager.routeEvent(request, c); err != nil {
			log.Println("error handling message", err)
		}
	}
}

func (c *Client) writeMessages() {
	defer func() {
		// cleanup conn
		c.manager.removeClient(c)
	}()

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case message, ok := <-c.egress:
			if !ok {
				if err := c.connection.Close(websocket.StatusAbnormalClosure, "egress closed"); err != nil {
					log.Println("connection closed: ", err)
				}
				return
			}

			data, err := json.Marshal(message)
			if err != nil {
				log.Println(err)
				return
			}

			if err := c.connection.Write(context.Background(), websocket.MessageText, data); err != nil {
				log.Println("failed to send message: ", err)
			}

			log.Println("message sent")
		case <-ticker.C:
			// log.Println("ping")
			ctx, cancel := context.WithTimeout(context.Background(), pongWait)
			err := c.connection.Ping(ctx)
			cancel()
			if err != nil {
				log.Println("ping timeout:", err)
				return
			}
			// log.Println("pong")
		}
	}
}
