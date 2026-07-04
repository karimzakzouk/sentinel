#!/usr/bin/env python3
"""
Aggressive AI slop cleanup for Sentinel Go codebase.
Deletes orphaned comments, file-level rants, field-level restatements,
and archaeology blocks. Preserves godoc comments (first comment on exported
identifiers) and inline comments that explain non-obvious logic.
"""
import re, sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
stats = {"deleted_lines": 0, "cleaned_lines": 0, "files_changed": 0}

def is_comment(line):
    """True if line is a comment (// or /* */ or * middle of block)."""
    s = line.strip()
    return s.startswith('//') or s.startswith('/*') or s.startswith('*')

def is_code(line):
    """True if line has actual code (not just comment or blank)."""
    s = line.strip()
    if not s or is_comment(s):
        return False
    return True

def is_orphaned_comment(lines, i):
    """True if line i is a comment NOT directly above code or a declaration.
    An orphaned comment is surrounded by blank lines above and below (or
    followed by another comment then blank)."""
    if not is_comment(lines[i]):
        return False
    
    # Check if this comment is directly above code (within 1 blank line)
    j = i + 1
    while j < len(lines) and is_comment(lines[j]):
        j += 1
    # j is now at the first non-comment line after the comment block
    if j < len(lines) and is_code(lines[j]):
        return False  # Comment is above code — keep it
    
    # Check if comment is above a declaration (func, var, type, const)
    # Allow 1 blank line between comment and declaration
    if j < len(lines) and lines[j].strip() == '' :
        k = j + 1
        while k < len(lines) and lines[k].strip() == '':
            k += 1
        if k < len(lines) and is_code(lines[k]):
            return False  # Comment is above code (with blank line) — keep it
    
    # Check above: if there's a blank line above, this is likely orphaned
    if i > 0 and lines[i-1].strip() == '':
        return True
    
    return False

def clean_file(path):
    """Clean slop from a single Go file."""
    try:
        content = path.read_text()
    except:
        return
    
    lines = content.split('\n')
    new_lines = []
    i = 0
    deleted = 0
    cleaned = 0
    
    while i < len(lines):
        line = lines[i]
        s = line.strip()
        
        # Delete: section divider comments (─── or ═══ or ----------)
        if re.match(r'^\s*//\s*[─═\-]{5,}', s):
            deleted += 1
            i += 1
            continue
        
        # Delete: "Prow's #N" references in comments
        if "prow's #" in s.lower() and is_comment(s):
            new_line = re.sub(r"(?i)\s*prow's #\d+\s+\w+", "", line)
            if new_line.strip() == '//':
                deleted += 1
                i += 1
                continue
            cleaned += 1
            new_lines.append(new_line)
            i += 1
            continue
        
        # Delete: "(audit #N)" / "(audit finding #N)" references
        if re.search(r'audit\s+(?:finding\s+)?#\d+', s, re.I) and is_comment(s):
            new_line = re.sub(r'(?i)\s*\(?\s*audit\s+(?:finding\s+)?#\d+\s*\)?', '', line)
            if new_line.strip() == '//':
                deleted += 1
                i += 1
                continue
            cleaned += 1
            new_lines.append(new_line)
            i += 1
            continue
        
        # Delete: "LEAK-N" / "obs-N" references
        if re.search(r'(?:LEAK-\d+|obs-\d+)', s) and is_comment(s):
            new_line = re.sub(r'\s*\(?(?:LEAK-\d+|obs-\d+)\)?', '', line)
            if new_line.strip() == '//':
                deleted += 1
                i += 1
                continue
            cleaned += 1
            new_lines.append(new_line)
            i += 1
            continue
        
        # Delete: version tags (v3.X.Y: / v4.X.Y: / v5.X.Y:)
        if re.search(r'v[345]\.\d+\.\d+:', s) and is_comment(s):
            new_line = re.sub(r'\s*v[345]\.\d+\.\d+:\s*', '', line)
            if new_line.strip() == '//':
                deleted += 1
                i += 1
                continue
            cleaned += 1
            new_lines.append(new_line)
            i += 1
            continue
        
        # Delete: H1/H2/H3.../M1/M4... fix references
        if re.search(r'[HM]\d+(?:\.\d+)?\s+fix:?', s, re.I) and is_comment(s):
            new_line = re.sub(r'(?i)\s*[HM]\d+(?:\.\d+)?\s+fix:?\s*', '', line)
            if new_line.strip() == '//':
                deleted += 1
                i += 1
                continue
            cleaned += 1
            new_lines.append(new_line)
            i += 1
            continue
        
        # Delete: "Was:" / "Previously:" / "Replaces:" lines within comments
        if re.match(r'^\s*//\s*(Was:|Previously:|Replaces:)', s, re.I):
            deleted += 1
            i += 1
            continue
        
        # Delete: "(MarshalJSON removed..." archaeology
        if 'MarshalJSON removed' in s and is_comment(s):
            deleted += 1
            i += 1
            continue
        
        # Delete: import guard junk
        if re.match(r'^\s*//\s*(missing\s+\w+\s+import\s+fix|strings\s+import\s+guard|unused import guard)', s, re.I):
            deleted += 1
            i += 1
            continue
        
        # Delete: "simpleHash32 is defined in..." cross-reference junk
        if 'is defined in' in s and 'shared between' in s and is_comment(s):
            deleted += 1
            i += 1
            continue
        
        # Delete: standalone "// value.NewAgentState." fragments
        if s == '// value.NewAgentState.' or s == '// value.':
            deleted += 1
            i += 1
            continue
        
        # Delete: broken fragments like "//iogo:106,255,"
        if re.match(r'^\s*//\s*[a-z]+go:\d+', s):
            deleted += 1
            i += 1
            continue
        
        # Delete: "(Claude's review noted..." AI review name-drops
        if "Claude's review" in s and is_comment(s):
            deleted += 1
            i += 1
            continue
        
        new_lines.append(line)
        i += 1
    
    # Remove consecutive blank lines (max 1)
    result = []
    prev_blank = False
    for line in new_lines:
        if line.strip() == '':
            if not prev_blank:
                result.append(line)
            prev_blank = True
        else:
            result.append(line)
            prev_blank = False
    
    new_content = '\n'.join(result)
    if new_content != content:
        path.write_text(new_content)
        stats["deleted_lines"] += deleted
        stats["cleaned_lines"] += cleaned
        stats["files_changed"] += 1
        if deleted > 0 or cleaned > 0:
            print(f"  {path.relative_to(REPO)}: -{deleted} cleaned:{cleaned}")

def main():
    for go_file in REPO.rglob("*.go"):
        if "vendor/" in str(go_file) or "node_modules/" in str(go_file):
            continue
        clean_file(go_file)
    
    print(f"\nTotal: {stats['files_changed']} files, {stats['deleted_lines']} lines deleted, {stats['cleaned_lines']} lines cleaned")

if __name__ == "__main__":
    main()
