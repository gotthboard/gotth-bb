package policy

type PostOwnership struct {
	AuthorID int64
}

type PostState string

const (
	PostVisible PostState = "visible"
	PostDeleted PostState = "deleted"
)

// CanEditPost admits only the active owner of one visible post. Staff roles do
// not grant authority to rewrite another author's words.
//
// Complexity: time and auxiliary space are tight Theta(1).
func CanEditPost(actor AccessContext, ownership PostOwnership, state PostState) bool {
	return actor.Valid() && actor.Authenticated && !actor.Suspended && actor.MutedUntil == nil &&
		ownership.AuthorID > 0 && actor.UserID == ownership.AuthorID && state == PostVisible
}
