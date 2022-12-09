package execution

// Controller implementations are used to control the k6 execution of a test or
// test suite, either locally or in a distributed environment.
type Controller interface {
	// TODO: split apart into `Once()`, `SetData(), `GetData()`?
	GetOrCreateData(id string, callback func() ([]byte, error)) ([]byte, error)

	Wait(eventId string) func() error
	Signal(eventId string) error
}

type namesspacedController struct {
	namespace string
	c         Controller
}

func (nc namesspacedController) GetOrCreateData(id string, callback func() ([]byte, error)) ([]byte, error) {
	return nc.c.GetOrCreateData(nc.namespace+"/"+id, callback)
}

func (nc namesspacedController) Wait(eventId string) func() error {
	return nc.c.Wait(nc.namespace + "/" + eventId)
}

func (nc namesspacedController) Signal(eventId string) error {
	return nc.c.Signal(nc.namespace + "/" + eventId)
}

func GetNamespacedController(namespace string, controller Controller) Controller {
	return &namesspacedController{namespace: namespace, c: controller}
}

func SignalAndWait(c Controller, eventID string) error {
	wait := c.Wait(eventID)
	if err := c.Signal(eventID); err != nil {
		return err
	}
	return wait()
}
