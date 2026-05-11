// Package store
package store

import (
	"docu-kiosk/broker/internal/domain"
	"fmt"

	"github.com/google/uuid"
)

type clientStore struct {
	clients map[uuid.UUID]domain.Client
}

func NewInMemoryClientStore() (*clientStore, error) {
	return &clientStore{
		clients: make(map[uuid.UUID]domain.Client),
	}, nil
}

func (cs *clientStore) SaveClient(client domain.Client) error {
	cs.clients[client.ID] = client
	return nil
}

func (cs *clientStore) RemoveClient(id uuid.UUID) error {
	if _, ok := cs.clients[id]; !ok {
		return fmt.Errorf("could not find client with id %s", id)
	}
	delete(cs.clients, id)
	return nil
}

func (cs *clientStore) GetCount() int {
	return len(cs.clients)
}

func (cs *clientStore) GetClientByID(id uuid.UUID) (domain.Client, error) {
	client, ok := cs.clients[id]
	if !ok {
		return domain.Client{}, fmt.Errorf("could not find client with id %s", id)
	}
	return client, nil
}
