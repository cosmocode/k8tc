// Package picker is the start-up target chooser: when k8tc is launched without a
// pod, it drills the user from namespace to pod (and container) before the file
// browser opens. It is a self-contained Bubble Tea program — main runs it first
// and reads back the chosen target — so the two-panel ui model never has to know
// about discovery.
//
// The list itself is a bubbles/list, but filtering is driven from a persistent
// search box rather than the list's built-in `/`-gated filter, so plain typing
// narrows the list and ordinary letters (including "q") are never swallowed as
// commands. Only Esc and Ctrl+C control the picker.
package picker

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/cosmocode/k8tc/internal/kube"
)

// discoverer enumerates what a session could target. *kube.Discovery satisfies
// it; tests inject a fake. The kube-context is passed per call (not bound) so
// the picker can switch contexts as the user drills. KubeconfigNamespaces only
// reads local kubeconfig and is the fallback when Namespaces is refused.
type discoverer interface {
	Contexts(context.Context) ([]string, error)
	CurrentContext(context.Context) (string, error)
	Namespaces(ctx context.Context, kubeCtx string) ([]string, error)
	Pods(ctx context.Context, kubeCtx, namespace string) ([]kube.PodInfo, error)
	KubeconfigNamespaces(ctx context.Context, kubeCtx string) ([]string, error)
}

// Selection is what the picker hands back: a fully-resolved target. Context is
// the chosen kube context ("" means the current one). Container is set
// explicitly for single-container pods too, so the resulting kubectl exec is
// unambiguous; it is empty only when the pod listed no containers.
type Selection struct {
	Context   string
	Namespace string
	Pod       string
	Container string
}

// level is which rung of the drill-down the list is showing.
type level int

const (
	levelContext level = iota
	levelNamespace
	levelPods
)

// item is one row. text is both the visible label and the value the search box
// filters against; the rest carries the target this row resolves to (only the
// field for the row's level is set).
type item struct {
	text      string
	kubeCtx   string
	namespace string
	pod       string
	container string
}

func (i item) Title() string       { return i.text }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return i.text }

// Model is the picker's Bubble Tea model.
type Model struct {
	disc        discoverer
	seed        string // namespace to start at (from -n); skips namespace rung
	startCtx    string // context from --context; skips the context rung
	hasStartCtx bool   // whether --context was given

	level       level
	kubeContext string // chosen context ("" = current), once past levelContext
	currentCtx  string // kubeconfig's current context, for preselection
	namespace   string // chosen namespace, once at levelPods

	list   list.Model
	search textinput.Model

	items    []item // full (unfiltered) set for the current level
	ctxItems []item // cached context rung, for Esc-back from namespaces
	nsItems  []item // cached namespace rung, for Esc-back from pods

	loading bool   // a listing is in flight
	typing  bool   // namespace free-text fallback (listing was refused)
	status  string // transient error line shown in place of the title

	width, height int
	boxWidth      int // inner content width of the centered dialog

	sel      Selection
	selected bool // a pod was chosen
	quit     bool // the user aborted
}

// New builds the picker. kubeContext (from --context) pins the context and skips
// the context rung; seedNamespace (from -n) jumps straight to that namespace's
// pods, skipping the namespace rung too.
func New(d discoverer, kubeContext, seedNamespace string) Model {
	ti := textinput.New()
	ti.Prompt = "Search: "
	ti.Placeholder = "type to filter"
	ti.CharLimit = 253
	ti.Focus()

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)

	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)   // the box height fits the items; no page splitting
	l.SetFilteringEnabled(false) // we filter via the search box ourselves
	l.DisableQuitKeybindings()

	m := Model{
		disc:        d,
		seed:        seedNamespace,
		startCtx:    kubeContext,
		hasStartCtx: kubeContext != "",
		kubeContext: kubeContext,
		list:        l,
		search:      ti,
		loading:     true,
	}
	switch {
	case seedNamespace != "":
		m.level = levelPods
		m.namespace = seedNamespace
	case m.hasStartCtx:
		m.level = levelNamespace
	default:
		m.level = levelContext
	}
	return m
}

// Result reports the chosen target, or ok=false if the user quit without one.
func (m Model) Result() (Selection, bool) {
	if !m.selected {
		return Selection{}, false
	}
	return m.sel, true
}

func (m Model) Init() tea.Cmd {
	switch {
	case m.seed != "":
		// -n given: jump straight to pods in the pinned/current context.
		return tea.Batch(textinput.Blink, loadPods(m.disc, m.kubeContext, m.seed))
	case m.hasStartCtx:
		// --context given: skip the context rung.
		return tea.Batch(textinput.Blink, loadNamespaces(m.disc, m.kubeContext))
	default:
		return tea.Batch(textinput.Blink, loadContexts(m.disc))
	}
}

// --- messages & commands ---

type contextsMsg struct {
	names   []string
	current string
	err     error
}

type namespacesMsg struct {
	names []string
	err   error
}

type kubeconfigMsg struct{ names []string }

type podsMsg struct {
	ns   string
	pods []kube.PodInfo
	err  error
}

func loadContexts(d discoverer) tea.Cmd {
	return func() tea.Msg {
		names, err := d.Contexts(context.Background())
		current, _ := d.CurrentContext(context.Background()) // best-effort
		return contextsMsg{names: names, current: current, err: err}
	}
}

func loadNamespaces(d discoverer, kubeCtx string) tea.Cmd {
	return func() tea.Msg {
		names, err := d.Namespaces(context.Background(), kubeCtx)
		return namespacesMsg{names: names, err: err}
	}
}

func loadKubeconfig(d discoverer, kubeCtx string) tea.Cmd {
	return func() tea.Msg {
		names, _ := d.KubeconfigNamespaces(context.Background(), kubeCtx)
		return kubeconfigMsg{names: names}
	}
}

func loadPods(d discoverer, kubeCtx, ns string) tea.Cmd {
	return func() tea.Msg {
		pods, err := d.Pods(context.Background(), kubeCtx, ns)
		return podsMsg{ns: ns, pods: pods, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case contextsMsg:
		// Only one context (or none, or listing failed): no point asking — pin it
		// and go straight to namespaces.
		if msg.err != nil || len(msg.names) <= 1 {
			if len(msg.names) == 1 {
				m.kubeContext = msg.names[0]
			}
			m.level = levelNamespace
			return m, loadNamespaces(m.disc, m.kubeContext)
		}
		m.currentCtx = msg.current
		cmd := m.showContexts(msg.names)
		return m, cmd

	case namespacesMsg:
		if msg.err != nil || len(msg.names) == 0 {
			// Listing refused or empty: drop to the typed-namespace fallback,
			// seeded with whatever the local kubeconfig knows.
			m.typing = true
			return m, loadKubeconfig(m.disc, m.kubeContext)
		}
		m.typing = false
		cmd := m.showNamespaces(msg.names)
		return m, cmd

	case kubeconfigMsg:
		m.typing = true
		cmd := m.showNamespaces(msg.names)
		return m, cmd

	case podsMsg:
		// Ignore a listing the user already navigated away from (Esc'd back).
		if m.level != levelPods || msg.ns != m.namespace {
			return m, nil
		}
		if msg.err != nil {
			m.status = "Couldn't list pods: " + msg.err.Error()
			m.loading = false
			m.items = nil
			m.fitList()
			cmd := m.applyFilter()
			return m, cmd
		}
		m.status = ""
		cmd := m.showPods(msg.pods)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Anything else (cursor blink, list internals) goes to both sub-models.
	var c1, c2 tea.Cmd
	m.search, c1 = m.search.Update(msg)
	m.list, c2 = m.list.Update(msg)
	return m, tea.Batch(c1, c2)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C always aborts; Esc is contextual — both work mid-load.
	if msg.Type == tea.KeyCtrlC {
		m.quit = true
		return m, tea.Quit
	}
	if msg.Type == tea.KeyEsc {
		return m.handleEsc()
	}
	if m.loading {
		return m, nil // ignore typing/navigation while a listing is in flight
	}

	switch msg.Type {
	case tea.KeyEnter:
		return m.handleEnter()
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	// Every other key edits the search box and re-filters the list.
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	fcmd := m.applyFilter()
	return m, tea.Batch(cmd, fcmd)
}

// handleEsc clears an active filter first; with no filter it backs up a level,
// and at the top level it exits.
func (m Model) handleEsc() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.search.Value()) != "" {
		m.search.SetValue("")
		cmd := m.applyFilter()
		return m, cmd
	}
	if m.level == levelPods && len(m.nsItems) > 0 {
		m.items = m.nsItems
		m.level = levelNamespace
		m.loading = false
		m.typing = false
		m.status = ""
		m.fitList()
		cmd := m.applyFilter()
		return m, cmd
	}
	if m.level == levelNamespace && len(m.ctxItems) > 0 {
		m.items = m.ctxItems
		m.level = levelContext
		m.loading = false
		m.typing = false
		m.status = ""
		m.fitList()
		cmd := m.applyFilter()
		m.selectByText(m.kubeContext) // land back on the context we drilled into
		return m, cmd
	}
	m.quit = true
	return m, tea.Quit
}

func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	sel, ok := m.list.SelectedItem().(item)
	switch m.level {
	case levelContext:
		if !ok {
			return m, nil
		}
		m.kubeContext = sel.kubeCtx
		m.level = levelNamespace
		m.loading = true
		m.status = ""
		m.search.SetValue("")
		return m, loadNamespaces(m.disc, m.kubeContext)

	case levelNamespace:
		var ns string
		switch {
		case ok:
			ns = sel.namespace
		case m.typing:
			// No suggestion matched the filter: take the typed text verbatim.
			ns = strings.TrimSpace(m.search.Value())
		}
		if ns == "" {
			return m, nil
		}
		m.namespace = ns
		m.level = levelPods
		m.loading = true
		m.status = ""
		m.search.SetValue("")
		return m, loadPods(m.disc, m.kubeContext, ns)

	case levelPods:
		if !ok {
			return m, nil
		}
		m.sel = Selection{
			Context:   m.kubeContext,
			Namespace: sel.namespace,
			Pod:       sel.pod,
			Container: sel.container,
		}
		m.selected = true
		return m, tea.Quit
	}
	return m, nil
}

// showContexts loads the context rung from names and caches it for Esc-back,
// landing the cursor on the in-effect context (the one already chosen, or
// kubeconfig's current) rather than the top of the list.
func (m *Model) showContexts(names []string) tea.Cmd {
	items := make([]item, 0, len(names))
	for _, n := range names {
		items = append(items, item{text: n, kubeCtx: n})
	}
	m.items = items
	m.ctxItems = items
	m.level = levelContext
	m.loading = false
	m.fitList()
	cmd := m.applyFilter() // resets the cursor to the top…
	preferred := m.kubeContext
	if preferred == "" {
		preferred = m.currentCtx
	}
	m.selectByText(preferred) // …then land it on the in-effect context
	return cmd
}

// selectByText moves the cursor to the visible row whose label is name, if any.
// It is a no-op for an empty name or no match (the cursor stays at the top).
func (m *Model) selectByText(name string) {
	if name == "" {
		return
	}
	for i, it := range m.list.Items() {
		if it.(item).text == name {
			m.list.Select(i)
			return
		}
	}
}

// showNamespaces loads the namespace rung from names and caches it for Esc-back.
func (m *Model) showNamespaces(names []string) tea.Cmd {
	items := make([]item, 0, len(names))
	for _, n := range names {
		items = append(items, item{text: n, namespace: n})
	}
	m.items = items
	m.nsItems = items
	m.level = levelNamespace
	m.loading = false
	m.fitList()
	return m.applyFilter()
}

// showPods loads the pod rung. A pod with one container is a single row (with
// that container resolved); a multi-container pod becomes one "pod : container"
// row each.
func (m *Model) showPods(pods []kube.PodInfo) tea.Cmd {
	items := make([]item, 0, len(pods))
	for _, p := range pods {
		switch len(p.Containers) {
		case 0:
			items = append(items, item{text: p.Name, namespace: m.namespace, pod: p.Name})
		case 1:
			items = append(items, item{text: p.Name, namespace: m.namespace, pod: p.Name, container: p.Containers[0]})
		default:
			for _, c := range p.Containers {
				items = append(items, item{
					text:      p.Name + " : " + c,
					namespace: m.namespace,
					pod:       p.Name,
					container: c,
				})
			}
		}
	}
	m.items = items
	m.level = levelPods
	m.loading = false
	m.fitList()
	return m.applyFilter()
}

// applyFilter pushes the current level's items, narrowed by the search box
// (case-insensitive substring), into the list. It resets the cursor to the top:
// SetItems does not clamp the old index, so without this a deeper selection
// could dangle past a now-shorter list (and SelectedItem would return nil).
func (m *Model) applyFilter() tea.Cmd {
	q := strings.ToLower(strings.TrimSpace(m.search.Value()))
	items := make([]list.Item, 0, len(m.items))
	for _, it := range m.items {
		if q == "" || strings.Contains(strings.ToLower(it.text), q) {
			items = append(items, it)
		}
	}
	cmd := m.list.SetItems(items)
	m.list.ResetSelected()
	return cmd
}

// --- view ---

var (
	// pickerDialogStyle is the centered modal box, matching the file browser's
	// dialog look (rounded purple border, one column of horizontal padding).
	pickerDialogStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("63")).
				Padding(0, 1)
	pickerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230"))
	pickerHelpStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// Box geometry. chromeLines is the non-list height inside the box (title, a
// blank, the search box, a blank, the help line). The list height is clamped to
// [minRows, maxRows] so the dialog is neither cramped nor sprawling.
const (
	chromeLines = 5
	minRows     = 3
	maxRows     = 14
)

// layout sets the dialog's width for the current terminal, then sizes the list.
func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Outer box adds 2 border + 2 padding columns, so the content caps at
	// width-4; keep it modest on wide terminals.
	cw := m.width - 4
	if cw > 72 {
		cw = 72
	}
	if cw < 24 {
		cw = 24
	}
	m.boxWidth = cw

	m.search.Width = cw - lipgloss.Width(m.search.Prompt) - 1
	if m.search.Width < 1 {
		m.search.Width = 1
	}
	m.fitList()
}

// fitList sizes the list to the current level's item count, clamped to a compact
// range and to what the terminal can fit. It is driven by the full (unfiltered)
// item count, so filtering within a level never resizes the box — only changing
// level (or the terminal) does.
func (m *Model) fitList() {
	rows := len(m.items)
	if rows < minRows {
		rows = minRows
	}
	if rows > maxRows {
		rows = maxRows
	}
	if max := m.height - chromeLines - 2; rows > max {
		rows = max
	}
	if rows < 1 {
		rows = 1
	}
	m.list.SetSize(m.boxWidth, rows)
}

func (m Model) View() string {
	if m.quit {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	var body string
	switch {
	case m.loading:
		body = lipgloss.NewStyle().Height(m.list.Height()).Render("  Loading…")
	case len(m.list.Items()) == 0:
		body = lipgloss.NewStyle().Height(m.list.Height()).Render("  (nothing to show)")
	default:
		body = m.list.View()
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		pickerTitleStyle.Render(clip(m.titleText(), m.boxWidth)),
		"",
		m.search.View(),
		body,
		"",
		pickerHelpStyle.Render(clip(m.helpText(), m.boxWidth)),
	)
	// Pin the content to the box width (so the box doesn't shrink to the longest
	// line), then border it and center it on an otherwise empty screen.
	inner := lipgloss.NewStyle().Width(m.boxWidth).Render(content)
	box := pickerDialogStyle.Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// clip truncates s to at most w display cells, adding an ellipsis if it had to
// cut, so a long title or error can't widen the dialog past its box.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

func (m Model) titleText() string {
	if m.status != "" {
		return m.status
	}
	switch {
	case m.typing:
		return "Type a namespace (cluster listing unavailable)"
	case m.level == levelContext:
		return "Select context"
	case m.level == levelPods:
		return "Select pod — " + m.namespace
	default:
		return "Select namespace"
	}
}

func (m Model) helpText() string {
	// Esc backs up a level when there's a rung behind us, otherwise it exits.
	tail := "esc/^C quit"
	if (m.level == levelPods && len(m.nsItems) > 0) ||
		(m.level == levelNamespace && len(m.ctxItems) > 0) {
		tail = "esc back · ^C quit"
	}
	if m.typing {
		return "↑/↓ move · enter use selected/typed namespace · " + tail
	}
	return "↑/↓ move · enter open · " + tail
}
