build: FORCE
	go build -o xbisect_bin .

install: build
	cp ./xbisect_bin /usr/local/bin/xbisect

FORCE: