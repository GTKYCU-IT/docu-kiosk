.PHONY: all build kiosk clean

all: build

kiosk:
	cd ../kiosk && npm run build

build: kiosk
	go build -o broker .

clean:
	rm -rf frontend/ broker
