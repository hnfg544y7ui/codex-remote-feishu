package control

// AsyncQuestionCardView describes a nonblocking question card. It does not
// create a native request or change the surface's request-capture state.
type AsyncQuestionCardView struct {
	ID          string
	Revision    int
	Questions   []AsyncQuestionCardQuestion
	CanSubmit   bool
	CanContinue bool
}

type AsyncQuestionCardQuestion struct {
	ID      string
	Title   string
	Options []string
}

func CloneAsyncQuestionCardView(view *AsyncQuestionCardView) *AsyncQuestionCardView {
	if view == nil {
		return nil
	}
	cloned := *view
	cloned.Questions = append([]AsyncQuestionCardQuestion(nil), view.Questions...)
	for i := range cloned.Questions {
		cloned.Questions[i].Options = append([]string(nil), view.Questions[i].Options...)
	}
	return &cloned
}
