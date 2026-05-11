package store

import (
	"docu-kiosk/broker/internal/domain"
	"testing"

	"github.com/google/uuid"
)

func TestAddClient(t *testing.T) {
	client := domain.Client{
		ID:   uuid.New(),
		Name: "test",
	}

	store, err := NewInMemoryClientStore()
	if err != nil {
		t.Errorf("failed to create store: %s", err)
	}

	initialCount := store.GetCount()
	if initialCount != 0 {
		t.Errorf("unexpected count. expected %d got %d", 0, initialCount)
	}

	store.SaveClient(client)

	finalCount := store.GetCount()
	if finalCount != 1 {
		t.Errorf("unexpected count. expected %d got %d", 1, finalCount)
	}
}

func TestGetClientByID(t *testing.T) {
	id := uuid.New()
	store, err := NewInMemoryClientStore()
	if err != nil {
		t.Errorf("failed to create store: %s", err)
	}
	store.SaveClient(domain.Client{
		ID:   id,
		Name: "test",
	})

	client, err := store.GetClientByID(id)
	if err != nil {
		t.Errorf("failed to get client: %s", err)
	}

	if client.Name != "test" {
		t.Errorf("wrong client. expected name %s got %s", "test", client.Name)
	}
}

func TestGetClientByIDBadValue(t *testing.T) {
	id := uuid.New()
	store, err := NewInMemoryClientStore()
	if err != nil {
		t.Errorf("failed to create store: %s", err)
	}
	store.SaveClient(domain.Client{
		ID:   id,
		Name: "test",
	})

	client, err := store.GetClientByID(uuid.New())
	if err == nil {
		t.Errorf("expected error got %s", client.ID)
	}
}

func TestDeleteClient(t *testing.T) {
	id := uuid.New()
	store, err := NewInMemoryClientStore()
	if err != nil {
		t.Errorf("failed to create store: %s", err)
	}
	store.SaveClient(domain.Client{
		ID:   id,
		Name: "test",
	})

	if err := store.RemoveClient(id); err != nil {
		t.Errorf("failed to remove client: %s", err)
	}
}

func TestDeleteClientBadValue(t *testing.T) {
	id := uuid.New()
	store, err := NewInMemoryClientStore()
	if err != nil {
		t.Errorf("failed to create store: %s", err)
	}
	store.SaveClient(domain.Client{
		ID:   id,
		Name: "test",
	})

	if err := store.RemoveClient(uuid.New()); err == nil {
		t.Errorf("expected error got %s", id)
	}
}
