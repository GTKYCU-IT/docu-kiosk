.PHONY: all build broker extension clean

all: build

build: broker extension

broker:
	$(MAKE) -C broker build

extension:
	cd extension && npm run build

clean:
	$(MAKE) -C broker clean
	cd extension && npm run build -- --emptyOutDir
