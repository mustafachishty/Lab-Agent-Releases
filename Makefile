# Lab Guardian Agent — Go Build Commands
# Cross-compile for Windows from any host OS.

.PHONY: build-windows build-debug clean

# Production build: stripped, no console window
build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o agent.exe .

# Debug build: with console window (useful for testing)
build-debug:
	GOOS=windows GOARCH=amd64 go build -o agent_debug.exe .

tidy:
	go mod tidy

clean:
	del /f agent.exe agent_debug.exe 2>nul || true
