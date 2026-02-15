# Traveler Makefile
# Usage:
#   make windows       — Windows 빌드
#   make linux-arm64   — Raspberry Pi 빌드
#   make all           — 둘 다 빌드
#   make deploy-pi     — Pi로 빌드 + 배포
#   make update-pi     — Pi 바이너리만 업데이트

PI_HOST ?= raspberrypi.local
PI_USER ?= pi

.PHONY: all windows linux-arm64 deploy-pi update-pi clean

all: windows linux-arm64

windows:
	GOOS=windows GOARCH=amd64 go build -o traveler.exe ./cmd/traveler/
	@echo "Built: traveler.exe"

linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o traveler-linux-arm64 ./cmd/traveler/
	@echo "Built: traveler-linux-arm64"

deploy-pi: linux-arm64
	bash scripts/deploy/deploy-to-pi.sh $(PI_HOST) $(PI_USER)

update-pi: linux-arm64
	bash scripts/deploy/update-pi.sh $(PI_HOST) $(PI_USER)

clean:
	rm -f traveler.exe traveler-linux-arm64
