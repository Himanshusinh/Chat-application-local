package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const maxPerChat = 5000

type FileEntry struct {
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
}

type TransferInfo struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Count     int         `json:"count"`
	Total     int64       `json:"total"` // bytes for one recipient
	Folder    bool        `json:"folder"`
	Files     []FileEntry `json:"files,omitempty"`  // first few, for display
	Status    string      `json:"status,omitempty"` // sending, receiving, done, failed, cancelled
	Done      int64       `json:"done,omitempty"`
	SavedPath string      `json:"savedPath,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type Message struct {
	ID       string        `json:"id"`
	Chat     string        `json:"chat"` // "all" or the other person's ID (local view)
	From     string        `json:"from"`
	FromName string        `json:"fromName"`
	Avatar   string        `json:"avatar,omitempty"`
	To       string        `json:"to"` // "all" or recipient ID
	Text     string        `json:"text,omitempty"`
	Time     int64         `json:"time"`
	Kind     string        `json:"kind"`             // "text", "sticker" or "files"
	Status   string        `json:"status,omitempty"` // outgoing DMs: pending, sent, read, failed
	Transfer *TransferInfo `json:"transfer,omitempty"`
	Sticker  *StickerRef   `json:"sticker,omitempty"`
}

type Store struct {
	mu     sync.Mutex
	path   string
	Chats  map[string][]*Message `json:"chats"`
	Unread map[string]int        `json:"unread"`
	ids    map[string]bool
	dirty  bool
}

var store *Store

func newStore(path string) *Store {
	s := &Store{path: path, Chats: map[string][]*Message{}, Unread: map[string]int{}, ids: map[string]bool{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Chats == nil {
			s.Chats = map[string][]*Message{}
		}
		if s.Unread == nil {
			s.Unread = map[string]int{}
		}
	}
	for _, list := range s.Chats {
		for _, m := range list {
			s.ids[m.ID] = true
			// Anything in flight when the app closed did not finish.
			if t := m.Transfer; t != nil && (t.Status == "sending" || t.Status == "receiving") {
				t.Status = "failed"
				t.Error = "interrupted"
			}
		}
	}
	return s
}

// add stores m and reports false if it was already stored (duplicate delivery).
func (s *Store) add(m *Message, unread bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids[m.ID] {
		return false
	}
	s.ids[m.ID] = true
	list := append(s.Chats[m.Chat], m)
	if len(list) > maxPerChat {
		for _, old := range list[:len(list)-maxPerChat] {
			delete(s.ids, old.ID)
		}
		list = append([]*Message(nil), list[len(list)-maxPerChat:]...)
	}
	s.Chats[m.Chat] = list
	if unread {
		s.Unread[m.Chat]++
	}
	s.dirty = true
	return true
}

func cloneMsg(m *Message) *Message {
	c := *m
	if m.Transfer != nil {
		t := *m.Transfer
		c.Transfer = &t
	}
	if m.Sticker != nil {
		st := *m.Sticker
		c.Sticker = &st
	}
	return &c
}

func (s *Store) messages(chat string, limit int) []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.Chats[chat]
	if len(list) > limit {
		list = list[len(list)-limit:]
	}
	out := make([]*Message, len(list))
	for i, m := range list {
		out[i] = cloneMsg(m)
	}
	return out
}

// update finds a message by ID and applies fn. Returns a copy for broadcasting.
func (s *Store) update(chat, id string, fn func(m *Message)) *Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.Chats[chat]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].ID == id {
			fn(list[i])
			s.dirty = true
			return cloneMsg(list[i])
		}
	}
	return nil
}

func (s *Store) findTransfer(tid string) *Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.Chats {
		for i := len(list) - 1; i >= 0; i-- {
			if list[i].Transfer != nil && list[i].Transfer.ID == tid {
				return cloneMsg(list[i])
			}
		}
	}
	return nil
}

func (s *Store) markRead(chat string) {
	s.mu.Lock()
	if s.Unread[chat] != 0 {
		s.Unread[chat] = 0
		s.dirty = true
	}
	s.mu.Unlock()
}

// markOutgoingRead flips our DMs to `peer` to "read".
func (s *Store) markOutgoingRead(peer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	me := getCfg().ID
	changed := false
	for _, m := range s.Chats[peer] {
		if m.From == me && m.Status == "sent" {
			m.Status = "read"
			changed = true
		}
	}
	if changed {
		s.dirty = true
	}
	return changed
}

func (s *Store) pending(peer string) []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Message
	for _, m := range s.Chats[peer] {
		if m.Status == "pending" {
			out = append(out, cloneMsg(m))
		}
	}
	return out
}

type chatSummary struct {
	Last   *Message `json:"last,omitempty"`
	Unread int      `json:"unread"`
}

func (s *Store) summaries() map[string]chatSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]chatSummary{}
	for chat, list := range s.Chats {
		cs := chatSummary{Unread: s.Unread[chat]}
		if len(list) > 0 {
			cs.Last = cloneMsg(list[len(list)-1])
			cs.Last.Transfer = nil
			if list[len(list)-1].Transfer != nil {
				cs.Last.Text = "📎 " + list[len(list)-1].Transfer.Name
			}
			if cs.Last.Kind == "sticker" {
				cs.Last.Text = "Sticker"
				cs.Last.Sticker = nil
			}
		}
		out[chat] = cs
	}
	return out
}

func (s *Store) clear(chat string) {
	s.mu.Lock()
	for _, m := range s.Chats[chat] {
		delete(s.ids, m.ID)
	}
	delete(s.Chats, chat)
	delete(s.Unread, chat)
	s.dirty = true
	s.mu.Unlock()
}

func (s *Store) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(s)
	s.dirty = false
	s.mu.Unlock()
	if err == nil {
		writeFileAtomic(s.path, b)
	}
}

func (s *Store) persistLoop() {
	for range time.Tick(2 * time.Second) {
		s.flush()
	}
}
