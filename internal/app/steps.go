package app

// runStep wraps a long-running operation with consistent start, success, and failure output.
func (a *App) runStep(title string, done string, run func() error) error {
	step := a.UI.Step(title)
	if err := run(); err != nil {
		step.Failed(err)
		return err
	}
	step.Done(done)
	return nil
}
