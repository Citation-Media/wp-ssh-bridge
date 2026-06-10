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

// runStepResult lets the operation choose the final success text after inspecting work done.
func (a *App) runStepResult(title string, run func() (string, error)) error {
	step := a.UI.Step(title)
	done, err := run()
	if err != nil {
		step.Failed(err)
		return err
	}
	step.Done(done)
	return nil
}
