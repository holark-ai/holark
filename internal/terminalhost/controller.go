package terminalhost

import (
	"context"
	"net"
	"time"
)

func (server *Server) attachController(id string, connection net.Conn, cancel context.CancelFunc) (*hostController, error) {
	server.controllerMu.Lock()
	if _, stale := server.fenced[id]; stale {
		server.controllerMu.Unlock()
		return nil, ErrStaleController
	}
	previous := server.controller
	if previous != nil && previous.id != id {
		server.fenced[previous.id] = struct{}{}
	} else if previous == nil && server.lastControllerID != "" && server.lastControllerID != id {
		// A disconnected incarnation may reconnect during the orphan grace, but
		// once a different node incarnation attaches it is permanently fenced.
		server.fenced[server.lastControllerID] = struct{}{}
	}
	server.nextEpoch++
	controller := &hostController{id: id, epoch: server.nextEpoch, connection: connection, cancel: cancel}
	server.controller = controller
	server.lastControllerID = id
	if server.orphanTimer != nil {
		server.orphanTimer.Stop()
		server.orphanTimer = nil
	}
	server.controllerMu.Unlock()

	if previous != nil {
		previous.cancel()
		_ = previous.connection.Close()
	}
	return controller, nil
}

func (server *Server) detachController(controller *hostController) {
	server.controllerMu.Lock()
	defer server.controllerMu.Unlock()
	if server.controller != controller {
		return
	}
	server.controller = nil
	server.startOrphanTimerLocked()
}

func (server *Server) controllerCurrent(controller *hostController) bool {
	server.controllerMu.Lock()
	defer server.controllerMu.Unlock()
	return server.controller == controller
}

func (server *Server) startOrphanTimerLocked() {
	if server.controller != nil || server.orphanTimer != nil {
		return
	}
	server.orphanTimer = time.AfterFunc(server.orphanGrace, server.expireOrphan)
}

func (server *Server) expireOrphan() {
	server.controllerMu.Lock()
	if server.controller != nil {
		server.orphanTimer = nil
		server.controllerMu.Unlock()
		return
	}
	server.orphanTimer = nil
	listener := server.listener
	server.controllerMu.Unlock()

	server.manager.Close()
	if server.onExpired != nil {
		server.onExpired()
	}
	server.expireOnce.Do(func() { close(server.expired) })
	if listener != nil {
		_ = listener.Close()
	}
}
