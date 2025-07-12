# Bump

`version=1`

This file is used to trigger the build process.

Unfortunately, the build system uses the GitHub code at the time the action runs. By then, the most recent code changes may not be available or included in the build.

To work around this, after committing the fixed code, we update (or "bump") this version file. This triggers the build again, allowing it to use the correct commit — i.e., the one before the bump.