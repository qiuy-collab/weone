package api

import (
	"context"
	"sort"
	"sync"

	"github.com/qiuy-collab/weone/ilink"
)

type MonitorStarter func(creds *ilink.Credentials)

type AccountRuntimeManager struct {
	mu             sync.RWMutex
	entries        map[string]*runtimeAccountEntry
	monitorStarter MonitorStarter
}

type runtimeAccountEntry struct {
	client *ilink.Client
	cancel context.CancelFunc
}

func NewAccountRuntimeManager() *AccountRuntimeManager {
	return &AccountRuntimeManager{entries: make(map[string]*runtimeAccountEntry)}
}

func (m *AccountRuntimeManager) SetMonitorStarter(starter MonitorStarter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.monitorStarter = starter
}

func (m *AccountRuntimeManager) Add(client *ilink.Client, cancel context.CancelFunc) {
	if client == nil {
		return
	}
	botID := client.BotID()
	if botID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.entries[botID]; ok && existing.cancel != nil {
		existing.cancel()
	}
	m.entries[botID] = &runtimeAccountEntry{client: client, cancel: cancel}
}

func (m *AccountRuntimeManager) AddCredentials(creds *ilink.Credentials) *ilink.Client {
	if creds == nil {
		return nil
	}
	client := ilink.NewClient(creds)
	m.mu.RLock()
	starter := m.monitorStarter
	m.mu.RUnlock()
	if starter != nil {
		starter(creds)
	}
	return client
}

func (m *AccountRuntimeManager) Remove(botID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[botID]
	if !ok {
		return false
	}
	delete(m.entries, botID)
	if entry.cancel != nil {
		entry.cancel()
	}
	return true
}

func (m *AccountRuntimeManager) Clients() []*ilink.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	clients := make([]*ilink.Client, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry != nil && entry.client != nil {
			clients = append(clients, entry.client)
		}
	}
	return clients
}

func (m *AccountRuntimeManager) BotIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.entries))
	for botID := range m.entries {
		ids = append(ids, botID)
	}
	sort.Strings(ids)
	return ids
}

func (m *AccountRuntimeManager) FirstClient() *ilink.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var first string
	for botID := range m.entries {
		if first == "" || botID < first {
			first = botID
		}
	}
	if first == "" {
		return nil
	}
	entry := m.entries[first]
	if entry == nil {
		return nil
	}
	return entry.client
}

func (m *AccountRuntimeManager) Client(botID string) *ilink.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.entries[botID]
	if entry == nil {
		return nil
	}
	return entry.client
}

func (m *AccountRuntimeManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

func (m *AccountRuntimeManager) Has(botID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.entries[botID]
	return ok
}
