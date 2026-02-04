@echo off
chcp 65001 >nul
REM Windows Auto-Login Setup

echo.
echo ========================================
echo  Windows Auto-Login Setup
echo ========================================
echo.
echo 1. netplwiz window will open
echo 2. UNCHECK "Users must enter a user name and password"
echo 3. Click OK and enter your password
echo.
pause

netplwiz

echo.
echo Done! Restart PC to verify auto-login works.
pause
