#!/usr/bin/env bash

# Script to install the precommit hook to your local git folder.
if [ ! -f .git/hooks/pre-commit ]; then
  echo "#!/usr/bin/env bash" >.git/hooks/pre-commit
fi

if ! grep -q "scripts/precommit.sh" .git/hooks/pre-commit; then
  echo "scripts/precommit.sh" >>.git/hooks/pre-commit
fi

chmod +x .git/hooks/pre-commit
chmod +x scripts/precommit.sh

echo "Pre-commit hook installed successfully."
