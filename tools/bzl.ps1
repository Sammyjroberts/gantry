# Dev helper: run bazelisk with a fully-resolved PATH (Machine + User) so fresh
# PowerShell sessions can find bazelisk, go, and git. Usage: tools\bzl.ps1 build //...
$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
& bazelisk @args
exit $LASTEXITCODE
