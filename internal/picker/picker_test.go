package picker

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cosmocode/k8tc/internal/kube"
)

// fakeDisc is a scripted discoverer. nsErr forces the typed-namespace fallback;
// pods is keyed by namespace. It ignores the kube-context argument — the tests
// that care about context assert what the model records, not what the fake does
// with it.
type fakeDisc struct {
	contexts   []string
	current    string
	ctxErr     error
	namespaces []string
	nsErr      error
	pods       map[string][]kube.PodInfo
	podErr     error
	kubeNS     []string
}

func (f fakeDisc) Contexts(context.Context) ([]string, error) {
	return f.contexts, f.ctxErr
}
func (f fakeDisc) CurrentContext(context.Context) (string, error) {
	return f.current, nil
}
func (f fakeDisc) Namespaces(context.Context, string) ([]string, error) {
	return f.namespaces, f.nsErr
}
func (f fakeDisc) Pods(_ context.Context, _, ns string) ([]kube.PodInfo, error) {
	if f.podErr != nil {
		return nil, f.podErr
	}
	return f.pods[ns], nil
}
func (f fakeDisc) KubeconfigNamespaces(context.Context, string) ([]string, error) {
	return f.kubeNS, nil
}

// send drives one message through Update and returns the concrete model + cmd.
func send(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// keyPress builds a KeyMsg for a special key.
func keyPress(typ tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: typ} }

// typeText feeds each rune of s to the model as a key press.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func itemTexts(m Model) []string {
	var out []string
	for _, it := range m.list.Items() {
		out = append(out, it.(item).text)
	}
	return out
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// ready returns a model parked at the namespace rung (context already resolved),
// the starting point for the namespace/pod tests.
func ready(t *testing.T, f fakeDisc) Model {
	t.Helper()
	m := New(f, "", "")
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = send(t, m, loadNamespaces(f, "")())
	return m
}

func TestNamespaceToPodSelection(t *testing.T) {
	f := fakeDisc{
		namespaces: []string{"prod", "dev"},
		pods: map[string][]kube.PodInfo{
			"prod": {
				{Name: "nginx", Containers: []string{"app"}},
				{Name: "redis", Containers: []string{"server", "exporter"}},
			},
		},
	}
	m := ready(t, f)

	if m.level != levelNamespace {
		t.Fatalf("level = %d, want namespace", m.level)
	}
	if got := itemTexts(m); len(got) != 2 || got[0] != "prod" {
		t.Fatalf("namespace items = %v, want [prod dev]", got)
	}

	// Enter on the first namespace kicks off the pod listing.
	m, cmd := send(t, m, keyPress(tea.KeyEnter))
	if m.level != levelPods || m.namespace != "prod" || !m.loading {
		t.Fatalf("after enter: level=%d ns=%q loading=%v", m.level, m.namespace, m.loading)
	}
	if cmd == nil {
		t.Fatal("expected a pod-load command")
	}
	m, _ = send(t, m, cmd()) // deliver podsMsg

	// One row per container: a multi-container pod expands.
	want := []string{"nginx", "redis : server", "redis : exporter"}
	got := itemTexts(m)
	if len(got) != len(want) {
		t.Fatalf("pod items = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pod items = %v, want %v", got, want)
		}
	}

	// Selecting the single-container pod resolves its container explicitly.
	m, cmd = send(t, m, keyPress(tea.KeyEnter))
	if !isQuit(cmd) {
		t.Fatal("selecting a pod should quit the picker")
	}
	sel, ok := m.Result()
	if !ok || sel != (Selection{Namespace: "prod", Pod: "nginx", Container: "app"}) {
		t.Fatalf("Result() = %+v, %v", sel, ok)
	}
}

func TestEscBacksUpThenExits(t *testing.T) {
	f := fakeDisc{
		namespaces: []string{"prod", "dev"},
		pods:       map[string][]kube.PodInfo{"prod": {{Name: "nginx", Containers: []string{"app"}}}},
	}
	m := ready(t, f)
	m, cmd := send(t, m, keyPress(tea.KeyEnter)) // into prod's pods
	m, _ = send(t, m, cmd())                     // deliver podsMsg
	if m.level != levelPods {
		t.Fatalf("expected to be at pod level, got %d", m.level)
	}

	// Esc with no filter backs up to the namespace list.
	m, _ = send(t, m, keyPress(tea.KeyEsc))
	if m.level != levelNamespace {
		t.Fatalf("Esc should return to namespace level, got %d", m.level)
	}
	if got := itemTexts(m); len(got) != 2 {
		t.Fatalf("namespace list not restored: %v", got)
	}

	// Esc again at the top exits.
	m, cmd = send(t, m, keyPress(tea.KeyEsc))
	if !isQuit(cmd) || !m.quit {
		t.Fatal("Esc at the top level should quit")
	}
	if _, ok := m.Result(); ok {
		t.Fatal("quitting must not yield a selection")
	}
}

func TestEscClearsFilterFirst(t *testing.T) {
	f := fakeDisc{namespaces: []string{"prod", "dev", "staging"}}
	m := ready(t, f)

	m = typeText(t, m, "dev")
	if got := itemTexts(m); len(got) != 1 || got[0] != "dev" {
		t.Fatalf("filter to 'dev' = %v", got)
	}

	// First Esc clears the filter rather than exiting.
	m, cmd := send(t, m, keyPress(tea.KeyEsc))
	if m.quit || isQuit(cmd) {
		t.Fatal("Esc with an active filter must not quit")
	}
	if got := itemTexts(m); len(got) != 3 {
		t.Fatalf("filter not cleared: %v", got)
	}
}

func TestCtrlCAlwaysQuits(t *testing.T) {
	m := ready(t, fakeDisc{namespaces: []string{"prod"}})
	m = typeText(t, m, "anything") // q-laden text must not quit on its own
	if m.quit {
		t.Fatal("typing should never quit")
	}
	m, cmd := send(t, m, keyPress(tea.KeyCtrlC))
	if !isQuit(cmd) || !m.quit {
		t.Fatal("Ctrl+C should quit")
	}
}

func TestTypedNamespaceFallback(t *testing.T) {
	f := fakeDisc{
		nsErr:  errors.New(`namespaces is forbidden`),
		kubeNS: []string{"myns", "other"},
		pods:   map[string][]kube.PodInfo{"zzz": {{Name: "web", Containers: []string{"c"}}}},
	}
	m := New(f, "", "")
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// A forbidden namespace listing flips to typing + a kubeconfig load.
	m, cmd := send(t, m, namespacesMsg{err: f.nsErr})
	if !m.typing {
		t.Fatal("forbidden listing should enable the typed fallback")
	}
	m, _ = send(t, m, cmd()) // deliver kubeconfigMsg
	if got := itemTexts(m); len(got) != 2 || got[0] != "myns" {
		t.Fatalf("kubeconfig suggestions = %v, want [myns other]", got)
	}

	// Typing a namespace that matches no suggestion, then Enter, uses the text.
	m = typeText(t, m, "zzz")
	if len(m.list.Items()) != 0 {
		t.Fatalf("expected no suggestions to match 'zzz', got %v", itemTexts(m))
	}
	m, cmd = send(t, m, keyPress(tea.KeyEnter))
	if m.namespace != "zzz" || m.level != levelPods {
		t.Fatalf("typed namespace not used: ns=%q level=%d", m.namespace, m.level)
	}
	m, _ = send(t, m, cmd()) // deliver podsMsg for "zzz"
	if got := itemTexts(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("pods for typed namespace = %v", got)
	}
}

func TestViewIsCenteredRectangle(t *testing.T) {
	const w, h = 80, 24
	m := ready(t, fakeDisc{namespaces: []string{"prod", "dev", "staging"}})
	view := m.View()

	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Fatalf("view has %d lines, want %d", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("line %d width = %d, want %d", i, got, w)
		}
	}
	// The dialog's rounded border should actually be drawn somewhere.
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╯") {
		t.Fatal("expected a rounded-border dialog box in the view")
	}
	// Centered, not flush against the top edge.
	if strings.TrimSpace(lines[0]) != "" {
		t.Fatalf("first line should be blank padding, got %q", lines[0])
	}
}

func TestSeedSkipsNamespaceStep(t *testing.T) {
	f := fakeDisc{pods: map[string][]kube.PodInfo{"prod": {{Name: "nginx", Containers: []string{"app"}}}}}
	m := New(f, "", "prod")
	if m.level != levelPods || m.namespace != "prod" {
		t.Fatalf("seed should start at pods for prod: level=%d ns=%q", m.level, m.namespace)
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = send(t, m, loadPods(f, "", "prod")()) // what Init would have fired
	if got := itemTexts(m); len(got) != 1 || got[0] != "nginx" {
		t.Fatalf("seeded pod list = %v", got)
	}

	// With no namespace rung behind it, Esc exits rather than backing up.
	m, cmd := send(t, m, keyPress(tea.KeyEsc))
	if !isQuit(cmd) || !m.quit {
		t.Fatal("Esc on a seeded picker should exit (no namespace list to return to)")
	}
}

func TestContextRungDrillDown(t *testing.T) {
	f := fakeDisc{
		contexts:   []string{"staging", "prod-cluster"},
		namespaces: []string{"prod"},
		pods:       map[string][]kube.PodInfo{"prod": {{Name: "nginx", Containers: []string{"app"}}}},
	}
	m := New(f, "", "")
	if m.level != levelContext {
		t.Fatalf("with no --context the picker should start at the context rung, got %d", m.level)
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = send(t, m, loadContexts(f)()) // what Init fires
	if got := itemTexts(m); len(got) != 2 || got[1] != "prod-cluster" {
		t.Fatalf("context items = %v, want [staging prod-cluster]", got)
	}

	// Choose the second context; namespaces load under it.
	m, _ = send(t, m, keyPress(tea.KeyDown))
	m, cmd := send(t, m, keyPress(tea.KeyEnter))
	if m.kubeContext != "prod-cluster" || m.level != levelNamespace {
		t.Fatalf("after choosing context: ctx=%q level=%d", m.kubeContext, m.level)
	}
	m, _ = send(t, m, cmd()) // namespacesMsg

	// Esc backs up to the context rung.
	m, _ = send(t, m, keyPress(tea.KeyEsc))
	if m.level != levelContext {
		t.Fatalf("Esc should return to the context rung, got %d", m.level)
	}

	// Drill all the way down; the chosen context rides along in the Selection.
	m, _ = send(t, m, keyPress(tea.KeyDown))
	m, cmd = send(t, m, keyPress(tea.KeyEnter)) // prod-cluster -> namespaces
	m, _ = send(t, m, cmd())
	m, cmd = send(t, m, keyPress(tea.KeyEnter)) // prod -> pods
	m, _ = send(t, m, cmd())
	m, cmd = send(t, m, keyPress(tea.KeyEnter)) // nginx -> done
	if !isQuit(cmd) {
		t.Fatal("selecting a pod should quit")
	}
	sel, ok := m.Result()
	want := Selection{Context: "prod-cluster", Namespace: "prod", Pod: "nginx", Container: "app"}
	if !ok || sel != want {
		t.Fatalf("Result() = %+v, want %+v", sel, want)
	}
}

func TestContextRungPreselectsCurrent(t *testing.T) {
	f := fakeDisc{contexts: []string{"staging", "prod", "dev"}, current: "prod"}
	m := New(f, "", "")
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = send(t, m, loadContexts(f)())

	if m.level != levelContext {
		t.Fatalf("expected the context rung, got %d", m.level)
	}
	sel, ok := m.list.SelectedItem().(item)
	if !ok || sel.kubeCtx != "prod" {
		t.Fatalf("current context not preselected: got %+v", sel)
	}

	// Pressing Enter with no other movement uses the preselected current context.
	m, cmd := send(t, m, keyPress(tea.KeyEnter))
	if m.kubeContext != "prod" {
		t.Fatalf("Enter on the preselected row chose %q, want prod", m.kubeContext)
	}
	_ = cmd
}

func TestSingleContextSkipsRung(t *testing.T) {
	f := fakeDisc{contexts: []string{"only"}, namespaces: []string{"prod"}}
	m := New(f, "", "")
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// One context: pin it and skip straight to namespaces.
	m, cmd := send(t, m, loadContexts(f)())
	if m.kubeContext != "only" || m.level != levelNamespace {
		t.Fatalf("single context not pinned/skipped: ctx=%q level=%d", m.kubeContext, m.level)
	}
	if cmd == nil {
		t.Fatal("expected a namespace-load command after skipping the context rung")
	}
	m, _ = send(t, m, cmd())
	if got := itemTexts(m); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("namespaces = %v", got)
	}

	// The rung was skipped, so there's nothing to back up to → Esc exits.
	m, cmd = send(t, m, keyPress(tea.KeyEsc))
	if !isQuit(cmd) || !m.quit {
		t.Fatal("Esc should exit when the context rung was skipped")
	}
}

func TestStartContextSkipsRung(t *testing.T) {
	f := fakeDisc{contexts: []string{"a", "b", "c"}, namespaces: []string{"prod"}}
	m := New(f, "given", "")
	if m.level != levelNamespace || m.kubeContext != "given" {
		t.Fatalf("--context should skip to the namespace rung: level=%d ctx=%q", m.level, m.kubeContext)
	}
	m, _ = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = send(t, m, loadNamespaces(f, "given")()) // what Init fires
	if got := itemTexts(m); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("namespaces = %v", got)
	}
	// No context rung was ever shown → Esc exits.
	m, cmd := send(t, m, keyPress(tea.KeyEsc))
	if !isQuit(cmd) || !m.quit {
		t.Fatal("Esc should exit; no context rung behind a --context start")
	}
}
