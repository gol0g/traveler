#!/bin/bash
# Add StandardOutput/StandardError to US and KR daemon services

# US service
sudo sed -i '/^NoNewPrivileges/i StandardOutput=append:/home/junghyun/.traveler/daemon_us.log\nStandardError=append:/home/junghyun/.traveler/daemon_us.log' /etc/systemd/system/traveler-us.service

# KR service
sudo sed -i '/^NoNewPrivileges/i StandardOutput=append:/home/junghyun/.traveler/daemon_kr.log\nStandardError=append:/home/junghyun/.traveler/daemon_kr.log' /etc/systemd/system/traveler-kr.service

sudo systemctl daemon-reload
echo "DONE"

# Verify
grep -A2 Standard /etc/systemd/system/traveler-us.service
echo "---"
grep -A2 Standard /etc/systemd/system/traveler-kr.service
