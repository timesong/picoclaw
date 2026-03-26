@echo off
setlocal EnableDelayedExpansion

REM PicoClaw Build Script for Windows
REM Default: Build for Windows
REM Usage: make.bat [target] [options]
REM Examples:
REM   make.bat build                    Build for Windows (default)
REM   make.bat build-linux              Build for Linux amd64
REM   make.bat build-linux-arm64        Build for Linux ARM64
REM   make.bat build-linux-arm          Build for Linux ARMv7
REM   make.bat build-all                Build for all platforms

set BINARY_NAME=picoclaw
set BUILD_DIR=build
set CMD_DIR=cmd\%BINARY_NAME%
set GO=go
set GOFLAGS=-v
set CGO_ENABLED=0

REM Get version info
for /f "tokens=*" %%i in ('git describe --tags --always --dirty 2^>nul') do set VERSION=%%i
if "%VERSION%"=="" set VERSION=dev

for /f "tokens=*" %%i in ('git rev-parse --short=8 HEAD 2^>nul') do set GIT_COMMIT=%%i
if "%GIT_COMMIT%"=="" set GIT_COMMIT=dev

for /f "tokens=*" %%i in ('date /t') do set BUILD_DATE=%%i
set BUILD_TIME=%BUILD_DATE%

set CONFIG_PKG=github.com/sipeed/picoclaw/pkg/config
set LDFLAGS=-X %CONFIG_PKG%.Version=%VERSION% -X %CONFIG_PKG%.GitCommit=%GIT_COMMIT% -X %CONFIG_PKG%.BuildTime=%BUILD_TIME% -s -w

set TARGET=%1

if "%TARGET%"=="" goto build-windows
if "%TARGET%"=="build" goto build-windows
if "%TARGET%"=="build-windows" goto build-windows
if "%TARGET%"=="build-linux" goto build-linux
if "%TARGET%"=="build-linux-amd64" goto build-linux-amd64
if "%TARGET%"=="build-linux-arm64" goto build-linux-arm64
if "%TARGET%"=="build-linux-arm" goto build-linux-arm
if "%TARGET%"=="build-linux-loong64" goto build-linux-loong64
if "%TARGET%"=="build-linux-riscv64" goto build-linux-riscv64
if "%TARGET%"=="build-linux-mipsle" goto build-linux-mipsle
if "%TARGET%"=="build-all" goto build-all
if "%TARGET%"=="clean" goto clean
if "%TARGET%"=="generate" goto generate
if "%TARGET%"=="help" goto help
if "%TARGET%"=="test" goto test
if "%TARGET%"=="deps" goto deps

echo Unknown target: %TARGET%
echo.
call :help
goto end

:generate
echo Running go generate...
rmdir /s /q %CMD_DIR%\workspace 2>nul
%GO% generate ./...
echo Generate complete.
goto end

:build-windows
echo Building %BINARY_NAME% for windows/amd64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-windows-amd64.exe" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-windows-amd64.exe
goto end

:build-linux
echo Building %BINARY_NAME% for linux/amd64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=amd64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-amd64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-amd64
goto end

:build-linux-amd64
echo Building %BINARY_NAME% for linux/amd64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=amd64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-amd64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-amd64
goto end

:build-linux-arm64
echo Building %BINARY_NAME% for linux/arm64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=arm64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-arm64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-arm64
goto end

:build-linux-arm
echo Building %BINARY_NAME% for linux/arm (GOARM=7)...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=arm
set GOMIPS=
set GOARM=7
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-arm" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-arm
goto end

:build-linux-loong64
echo Building %BINARY_NAME% for linux/loong64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=loong64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-loong64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-loong64
goto end

:build-linux-riscv64
echo Building %BINARY_NAME% for linux/riscv64...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=riscv64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-riscv64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-riscv64
goto end

:build-linux-mipsle
echo Building %BINARY_NAME% for linux/mipsle (softfloat)...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=mipsle
set GOMIPS=softfloat
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-mipsle" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-mipsle
goto end

:build-all
echo Building for all platforms...
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"

REM Linux builds
echo.
echo === Linux builds ===
call :build-linux-amd64-internal
call :build-linux-arm64-internal
call :build-linux-arm-internal
call :build-linux-loong64-internal
call :build-linux-riscv64-internal
call :build-linux-mipsle-internal

REM Darwin builds
echo.
echo === Darwin builds ===
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=darwin
set GOARCH=arm64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-darwin-arm64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-darwin-arm64

REM Windows builds
echo.
echo === Windows builds ===
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=windows
set GOARCH=amd64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-windows-amd64.exe" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-windows-amd64.exe

echo.
echo All builds complete!
goto end

:build-linux-amd64-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=amd64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-amd64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-amd64
goto :eof

:build-linux-arm64-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=arm64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-arm64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-arm64
goto :eof

:build-linux-arm-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=arm
set GOARM=7
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-arm" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-arm
goto :eof

:build-linux-loong64-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=loong64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-loong64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-loong64
goto :eof

:build-linux-riscv64-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=riscv64
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-riscv64" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-riscv64
goto :eof

:build-linux-mipsle-internal
if not exist "%BUILD_DIR%" mkdir "%BUILD_DIR%"
set GOOS=linux
set GOARCH=mipsle
set GOMIPS=softfloat
%GO% build %GOFLAGS% -ldflags "%LDFLAGS%" -o "%BUILD_DIR%\%BINARY_NAME%-linux-mipsle" .\%CMD_DIR%
echo Build complete: %BUILD_DIR%\%BINARY_NAME%-linux-mipsle
goto :eof

:clean
echo Cleaning build artifacts...
rmdir /s /q "%BUILD_DIR%" 2>nul
echo Clean complete.
goto end

:test
echo Running tests...
%GO% test $(go list ./... | grep -v github.com/sipeed/picoclaw/web/)
goto end

:deps
echo Downloading dependencies...
%GO% mod download
%GO% mod verify
echo Dependencies verified.
goto end

:help
echo.
echo PicoClaw Build Script for Windows
echo.
echo Usage:
echo   make.bat [target]
echo.
echo Targets:
echo   build, build-windows      Build for Windows amd64 (default)
echo   build-linux               Build for Linux amd64
echo   build-linux-amd64         Build for Linux amd64
echo   build-linux-arm64         Build for Linux ARM64
echo   build-linux-arm           Build for Linux ARMv7 (Raspberry Pi)
echo   build-linux-loong64       Build for Linux LoongArch64
echo   build-linux-riscv64       Build for Linux RISC-V 64
echo   build-linux-mipsle        Build for Linux MIPS32 LE
echo   build-all                 Build for all platforms
echo   generate                  Run go generate
echo   clean                     Remove build artifacts
echo   deps                      Download and verify dependencies
echo   test                      Run tests
echo   help                      Show this help message
echo.
echo Examples:
echo   make.bat                          Build for Windows (default)
echo   make.bat build-linux              Build for Linux amd64
echo   make.bat build-linux-arm64        Build for Linux ARM64
echo   make.bat build-all                Build for all platforms
echo.
goto end

:end
endlocal
