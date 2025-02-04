package posts

import (
	"errors"
	"fmt"

	pager "github.com/charmbracelet/bubbles/paginator"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/reflow/indent"
	"github.com/neurosnap/lists.sh/internal"
	"github.com/neurosnap/lists.sh/internal/db"
	"github.com/neurosnap/lists.sh/internal/ui/common"
	"go.uber.org/zap"
)

const keysPerPage = 4

type state int

const (
	stateInitCharmClient state = iota
	stateLoading
	stateNormal
	stateDeletingPost
	stateDeletingActivePost
	stateDeletingAccount
	stateQuitting
)

type postState int

const (
	postNormal postState = iota
	postSelected
	postDeleting
)

// NewProgram creates a new Tea program.
func NewProgram(dbpool db.DB, user *db.User) *tea.Program {
	m := NewModel(dbpool, user)
	m.standalone = true
	return tea.NewProgram(m)
}

type PostLoader struct {
	Posts []*db.Post
}

type (
	postsLoadedMsg PostLoader
	removePostMsg  int
	errMsg         struct {
		err error
	}
)

// Model is the Tea state model for this user interface.
type Model struct {
	dbpool     db.DB
	user       *db.User
	posts      []*db.Post
	styles     common.Styles
	pager      pager.Model
	state      state
	err        error
	standalone bool
	index      int // index of selected key in relation to the current page
	Exit       bool
	Quit       bool
	spinner    spinner.Model
	logger     *zap.SugaredLogger
}

// getSelectedIndex returns the index of the cursor in relation to the total
// number of items.
func (m *Model) getSelectedIndex() int {
	return m.index + m.pager.Page*m.pager.PerPage
}

// UpdatePaging runs an update against the underlying pagination model as well
// as performing some related tasks on this model.
func (m *Model) UpdatePaging(msg tea.Msg) {
	// Handle paging
	m.pager.SetTotalPages(len(m.posts))
	m.pager, _ = m.pager.Update(msg)

	// If selected item is out of bounds, put it in bounds
	numItems := m.pager.ItemsOnPage(len(m.posts))
	m.index = min(m.index, numItems-1)
}

// NewModel creates a new model with defaults.
func NewModel(dbpool db.DB, user *db.User) Model {
	logger := internal.CreateLogger()
	st := common.DefaultStyles()

	p := pager.NewModel()
	p.PerPage = keysPerPage
	p.Type = pager.Dots
	p.InactiveDot = st.InactivePagination.Render("•")

	return Model{
		dbpool:  dbpool,
		user:    user,
		styles:  st,
		pager:   p,
		state:   stateLoading,
		err:     nil,
		posts:   []*db.Post{},
		index:   0,
		spinner: common.NewSpinner(),
		Exit:    false,
		Quit:    false,
		logger:  logger,
	}
}

// Init is the Tea initialization function.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		spinner.Tick,
	)
}

// Update is the tea update function which handles incoming messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			if m.standalone {
				m.state = stateQuitting
				return m, tea.Quit
			}
			m.Exit = true
			return m, nil

		// Select individual items
		case "up", "k":
			// Move up
			m.index--
			if m.index < 0 && m.pager.Page > 0 {
				m.index = m.pager.PerPage - 1
				m.pager.PrevPage()
			}
			m.index = max(0, m.index)
		case "down", "j":
			// Move down
			itemsOnPage := m.pager.ItemsOnPage(len(m.posts))
			m.index++
			if m.index > itemsOnPage-1 && m.pager.Page < m.pager.TotalPages-1 {
				m.index = 0
				m.pager.NextPage()
			}
			m.index = min(itemsOnPage-1, m.index)

		// Delete
		case "x":
			if len(m.posts) > 0 {
				m.state = stateDeletingPost
				m.UpdatePaging(msg)
			}

			return m, nil

		// Confirm Delete
		case "y":
			switch m.state {
			case stateDeletingPost:
				m.state = stateNormal
				return m, removePost(m)
			}
		}

	case errMsg:
		m.err = msg.err
		return m, nil

	case postsLoadedMsg:
		m.state = stateNormal
		m.index = 0
		m.posts = msg.Posts

	case removePostMsg:
		if m.state == stateQuitting {
			return m, tea.Quit
		}
		i := m.getSelectedIndex()

		// Remove key from array
		m.posts = append(m.posts[:i], m.posts[i+1:]...)

		// Update pagination
		m.pager.SetTotalPages(len(m.posts))
		m.pager.Page = min(m.pager.Page, m.pager.TotalPages-1)

		// Update cursor
		m.index = min(m.index, m.pager.ItemsOnPage(len(m.posts)-1))

		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		if m.state < stateNormal {
			m.spinner, cmd = m.spinner.Update(msg)
		}
		return m, cmd
	}

	m.UpdatePaging(msg)

	// If an item is being confirmed for delete, any key (other than the key
	// used for confirmation above) cancels the deletion
	k, ok := msg.(tea.KeyMsg)
	if ok && k.String() != "x" {
		m.state = stateNormal
	}

	return m, nil
}

// View renders the current UI into a string.
func (m Model) View() string {
	if m.err != nil {
		return m.err.Error()
	}

	var s string

	switch m.state {
	case stateLoading:
		s = m.spinner.View() + " Loading...\n\n"
	case stateQuitting:
		s = "Thanks for using lists.sh!\n"
	default:
		s = "Here are the posts linked to your account.\n\n"

		s += postsView(m)
		if m.pager.TotalPages > 1 {
			s += m.pager.View()
		}

		// Footer
		switch m.state {
		case stateDeletingPost:
			s += m.promptView("Delete this post?")
		default:
			s += "\n\n" + helpView(m)
		}
	}

	if m.standalone {
		return indent.String(fmt.Sprintf("\n%s\n", s), 2)
	}
	return s
}

func postsView(m Model) string {
	var (
		s          string
		state      postState
		start, end = m.pager.GetSliceBounds(len(m.posts))
		slice      = m.posts[start:end]
	)

	destructiveState := m.state == stateDeletingPost

	if len(m.posts) == 0 {
		s += "You don't have any posts yet."
		return s
	}

	// Render key info
	for i, post := range slice {
		if destructiveState && m.index == i {
			state = postDeleting
		} else if m.index == i {
			state = postSelected
		} else {
			state = postNormal
		}
		s += m.newStyledKey(m.styles, post).render(state)
	}

	// If there aren't enough keys to fill the view, fill the missing parts
	// with whitespace
	if len(slice) < m.pager.PerPage {
		for i := len(slice); i < keysPerPage; i++ {
			s += "\n\n\n"
		}
	}

	return s
}

func helpView(m Model) string {
	var items []string
	if len(m.posts) > 1 {
		items = append(items, "j/k, ↑/↓: choose")
	}
	if m.pager.TotalPages > 1 {
		items = append(items, "h/l, ←/→: page")
	}
	if len(m.posts) > 0 {
		items = append(items, "x: delete")
	}
	items = append(items, "esc: exit")
	return common.HelpView(items...)
}

func (m Model) promptView(prompt string) string {
	st := m.styles.Delete.Copy().MarginTop(2).MarginRight(1)
	return st.Render(prompt) +
		m.styles.DeleteDim.Render("(y/N)")
}

func LoadPosts(m Model) tea.Cmd {
	if m.user == nil {
		m.logger.Info("user not found!")
		err := errors.New("user not found")
		return func() tea.Msg {
			return errMsg{err}
		}
	}
	if m.standalone {
		return fetchPosts(m.dbpool, m.user.ID)
	}
	return tea.Batch(
		fetchPosts(m.dbpool, m.user.ID),
		spinner.Tick,
	)
}

func fetchPosts(dbpool db.DB, userID string) tea.Cmd {
	return func() tea.Msg {
		posts, _ := dbpool.PostsForUser(userID)
		loader := PostLoader{
			Posts: posts,
		}
		return postsLoadedMsg(loader)
	}
}

func removePost(m Model) tea.Cmd {
	return func() tea.Msg {
		err := m.dbpool.RemovePosts([]string{m.posts[m.getSelectedIndex()].ID})
		if err != nil {
			return errMsg{err}
		}
		return removePostMsg(m.index)
	}
}

// Utils

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
