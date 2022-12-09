package local

// Controller controls local tests.
type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

func (c *Controller) GetOrCreateData(id string, callback func() ([]byte, error)) ([]byte, error) {
	return callback()
}

func (c *Controller) Wait(eventId string) func() error {
	// TODO: actually use waitgroups
	return func() error {
		return nil
	}
}
func (c *Controller) Signal(eventId string) error {
	return nil
}
