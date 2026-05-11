// Package domain
package domain

import "github.com/google/uuid"

type Client struct {
	ID   uuid.UUID
	Name string
}

type ClientStore interface {
	SaveClient(client Client) error
	RemoveClient(id uuid.UUID) error
	GetClientByID(id uuid.UUID) (Client, error)
	GetCount() int
}

func NewClient(name string) Client {
	return Client{
		ID:   uuid.New(),
		Name: name,
	}
}
