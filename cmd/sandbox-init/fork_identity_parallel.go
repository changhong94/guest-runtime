package main

// joinForkIdentity always joins the worker, including when the foreground
// operation fails. No child lifecycle transition may race unfinished cleanup.
func joinForkIdentity(sockets, entropy func() error) error {
	done := make(chan error, 1)
	go func() { done <- entropy() }()
	socketErr := sockets()
	entropyErr := <-done
	if socketErr != nil {
		return socketErr
	}
	return entropyErr
}
