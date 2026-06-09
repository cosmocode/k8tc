package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap holds every binding k8tc reacts to.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	PgUp    key.Binding
	PgDn    key.Binding
	Tab     key.Binding
	Enter   key.Binding
	Copy    key.Binding
	Refresh key.Binding
	Quit    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k")),
		Down:    key.NewBinding(key.WithKeys("down", "j")),
		PgUp:    key.NewBinding(key.WithKeys("pgup")),
		PgDn:    key.NewBinding(key.WithKeys("pgdown")),
		Tab:     key.NewBinding(key.WithKeys("tab")),
		Enter:   key.NewBinding(key.WithKeys("enter")),
		Copy:    key.NewBinding(key.WithKeys("f5", "c")),
		Refresh: key.NewBinding(key.WithKeys("r")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c")),
	}
}
