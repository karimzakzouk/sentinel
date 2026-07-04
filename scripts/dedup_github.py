#!/usr/bin/env python3
"""Delete duplicate GitHub methods from chatops/github.go and automerge/github.go.
These are now inherited from BaseClient via embedding."""
import re
from pathlib import Path

def delete_methods(path, method_names):
    """Delete specified methods from a Go file."""
    content = Path(path).read_text()
    lines = content.split('\n')
    new_lines = []
    i = 0
    deleted = 0
    
    while i < len(lines):
        # Check if this line starts a method we want to delete
        line = lines[i]
        match = False
        for name in method_names:
            # Match: func (g *GitHubClientImpl) MethodName(
            pattern = rf'^func \(g \*GitHubClientImpl\) {name}\('
            if re.match(pattern, line.strip()):
                # Find the end of this function (matching braces)
                brace_count = 0
                j = i
                while j < len(lines):
                    brace_count += lines[j].count('{') - lines[j].count('}')
                    if brace_count == 0 and j > i:
                        break
                    j += 1
                # Delete from i to j (inclusive), plus any preceding comment
                # Check if previous line is a comment
                start = i
                while start > 0 and new_lines and new_lines[-1].strip().startswith('//'):
                    new_lines.pop()
                # Also remove trailing blank line
                i = j + 1
                if i < len(lines) and lines[i].strip() == '':
                    i += 1
                deleted += (j - start + 1)
                match = True
                break
        
        if not match:
            new_lines.append(line)
            i += 1
    
    new_content = '\n'.join(new_lines)
    if new_content != content:
        Path(path).write_text(new_content)
        print(f"  {path}: deleted {deleted} lines ({len(method_names)} methods)")
    else:
        print(f"  {path}: no changes")

# Delete from automerge: all 5 methods (inherited from BaseClient)
delete_methods("internal/automerge/github.go", ["PostComment", "SetLabels", "RemoveLabel", "GetLabels", "Merge"])

# Delete from chatops: 4 methods (keep PostComment — it calls PostCommentWithID)
delete_methods("internal/chatops/github.go", ["SetLabels", "RemoveLabel", "GetLabels", "Merge"])
