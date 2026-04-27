@echo off
echo ========================================================
echo   Lab Guardian Agent - Hidden Console Build Tool
echo ========================================================
echo.
echo [1/3] Cleaning up old artifacts...
del agent.exe 2>nul
del rsrc.syso 2>nul

echo [2/3] Generating Windows Resources (Icons/Manifest)...
go install github.com/akavel/rsrc@latest
rsrc -manifest app.manifest -ico icon.ico -o rsrc.syso

echo [3/3] Compiling Agent (Hidden Window Mode)...
:: -H=windowsgui hides the CMD window when the EXE is launched
go build -ldflags="-s -w -H=windowsgui" -o agent.exe .

if %ERRORLEVEL% EQU 0 (
    echo.
    echo ========================================================
    echo SUCCESS: agent.exe created successfully.
    echo You can now run it without any terminal window showing!
    echo ========================================================
) else (
    echo.
    echo ERROR: Compilation failed. Please check your Go environment.
)
pause
