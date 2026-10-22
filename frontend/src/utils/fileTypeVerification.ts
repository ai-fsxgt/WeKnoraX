const DEFAULT_VALID_TYPES = new Set([
  "pdf",
  "txt",
  "md",
  "docx",
  "doc",
  "pptx",
  "ppt",
  "epub",
  "html",
  "htm",
  "mhtml",
  "jpg",
  "jpeg",
  "png",
  "mp3",
  "wav",
  "m4a",
  "flac",
  "ogg",
]);

// 表格类文件已停止解析，即使解析引擎声明支持也一律拒绝
const BLOCKED_TYPES = new Set(["csv", "xlsx", "xls"]);

export function getAllowedKnowledgeFileTypes(
  validTypes?: Set<string> | string[],
): string[] {
  const provided = validTypes
    ? validTypes instanceof Set
      ? validTypes
      : new Set(validTypes)
    : undefined;
  const allowed = provided && provided.size > 0 ? provided : DEFAULT_VALID_TYPES;

  return [...allowed].filter(type => !BLOCKED_TYPES.has(type));
}

export function shouldRejectKnowledgeFileType(
  filename: string,
  validTypes?: Set<string> | string[],
) {
  const type = filename.substring(filename.lastIndexOf(".") + 1).toLowerCase();

  return !getAllowedKnowledgeFileTypes(validTypes).includes(type);
}
