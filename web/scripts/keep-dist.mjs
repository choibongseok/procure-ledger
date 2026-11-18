// vite build 는 dist 를 비우므로, go:embed 가 빈 디렉터리 때문에 깨지지 않도록 .gitkeep 을 되살린다.
import { writeFileSync } from "node:fs";
writeFileSync(new URL("../dist/.gitkeep", import.meta.url), "");
