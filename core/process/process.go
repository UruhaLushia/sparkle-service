package process

// Controller owns the launched core's process group or job object.
type Controller interface {
	Attach(pid int32) error
	PIDs() ([]int32, error)
	Stop(pid int32) error
	Close() error
}
