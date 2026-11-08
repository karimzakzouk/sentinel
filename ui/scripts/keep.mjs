// vite empties the output directory; the Go embed directive needs it to
// exist in a fresh clone, so it keeps a tracked placeholder.
import { writeFileSync } from "node:fs";

writeFileSync(new URL("../../internal/ui/dist/.gitkeep", import.meta.url), "");
