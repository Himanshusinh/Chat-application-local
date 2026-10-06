package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIncognitoIsNotSaved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s := newStore(path)
	if !s.add(&Message{ID: "a", Chat: "p", From: "me", To: "p", Text: "hello", Kind: "text", Time: 1}, false) {
		t.Fatal("add")
	}
	if !s.add(&Message{ID: "b", Chat: "p", From: "me", To: "p", Text: "secret words", Kind: "text", Incognito: true, Time: 2}, true) {
		t.Fatal("add incognito")
	}
	if s.add(&Message{ID: "b", Chat: "p", From: "me", To: "p", Text: "secret words", Kind: "text", Incognito: true, Time: 2}, false) {
		t.Fatal("duplicate incognito was stored again")
	}
	s.flush()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatalf("incognito text was written to disk: %s", b)
	}
	if !strings.Contains(string(b), "hello") {
		t.Fatal("normal message missing")
	}
	if got := s.messages("p", 10); len(got) != 2 || got[1].Text != "secret words" {
		t.Fatalf("live view = %+v", got)
	}
	again := newStore(path)
	if got := again.messages("p", 10); len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("after restart = %+v", got)
	}
}

func TestEditAndDelete(t *testing.T) {
	s := newStore(filepath.Join(t.TempDir(), "h.json"))
	s.add(&Message{ID: "a", Chat: "p", From: "me", To: "p", Text: "one", Kind: "text", Time: 1}, false)
	if u := s.update("p", "a", func(m *Message) { m.Text = "two"; m.Edited = true }); u == nil || u.Text != "two" {
		t.Fatal("edit")
	}
	if _, ok := s.removeID("a"); !ok {
		t.Fatal("remove")
	}
	if s.get("a") != nil || len(s.messages("p", 10)) != 0 {
		t.Fatal("still there")
	}
	s.add(&Message{ID: "c", Chat: "p", From: "me", To: "p", Text: "tmp", Kind: "text", Incognito: true, Time: 3}, false)
	s.update("p", "c", func(m *Message) { m.Text = "changed" })
	s.flush()
	if b, _ := os.ReadFile(s.path); strings.Contains(string(b), "changed") || strings.Contains(string(b), "tmp") {
		t.Fatalf("incognito edit was saved: %s", b)
	}
}
