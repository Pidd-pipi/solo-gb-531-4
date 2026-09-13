package constants

type RectificationState string

const (
	RectificationPending       RectificationState = "pending"
	RectificationInProgress    RectificationState = "in_progress"
	RectificationPendingReview RectificationState = "pending_review"
	RectificationCompleted     RectificationState = "completed"
	RectificationVoided        RectificationState = "voided"
)

var rectificationTransitions = map[RectificationState]map[RectificationState]struct{}{
	RectificationPending:       {RectificationInProgress: {}, RectificationVoided: {}},
	RectificationInProgress:    {RectificationPendingReview: {}, RectificationVoided: {}},
	RectificationPendingReview: {RectificationCompleted: {}, RectificationInProgress: {}, RectificationVoided: {}},
	RectificationCompleted:     {},
	RectificationVoided:        {},
}

func (s RectificationState) Valid() bool {
	_, ok := rectificationTransitions[s]
	return ok
}

func CanTransitionRectification(from, to RectificationState) bool {
	allowed, ok := rectificationTransitions[from]
	if !ok {
		return false
	}
	_, ok = allowed[to]
	return ok
}

func RectificationStateValues() []string {
	return []string{"pending", "in_progress", "pending_review", "completed", "voided"}
}
