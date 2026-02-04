@echo off
REM Traveler Daemon Mode Starter
REM 이 파일을 시작 프로그램에 등록하거나 Task Scheduler에서 실행

cd /d "%~dp0\.."
echo Starting Traveler Daemon Mode...
echo.
echo Daily Target: 1%%
echo Loss Limit: -2%%
echo.

REM 데몬 모드 실행
traveler.exe --daemon --config config.yaml

REM 오류 발생시 대기 (디버깅용)
if %ERRORLEVEL% neq 0 (
    echo.
    echo Daemon exited with error code: %ERRORLEVEL%
    pause
)
