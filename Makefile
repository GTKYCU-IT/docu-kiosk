.PHONY: all build web extension clean dev dev-web dev-broker

all: build

build: web broker extension

dev:
	$(MAKE) -j2 dev-web dev-broker

dev-web:
	cd web && npm run dev

dev-broker:
	air

web:
	cd web && npm run build

broker: web
	go build -o broker .

extension:
	cd extension && npm run build

clean:
	rm -f broker
	rm -rf web/dist tmp
	cd extension && npm run build -- --emptyOutDir
