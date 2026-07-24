import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";

interface Props {
  children: string;
}

/** Markdown renders sanitized markdown (challenge descriptions, event blurb). */
export function Markdown({ children }: Props) {
  return (
    <div className="prose-invert max-w-none space-y-3 text-text [&_a]:text-primary [&_a]:underline [&_code]:rounded [&_code]:bg-surface-2 [&_code]:px-1 [&_code]:font-mono [&_code]:text-sm [&_h1]:text-xl [&_h1]:font-bold [&_h2]:text-lg [&_h2]:font-semibold [&_li]:ml-4 [&_li]:list-disc [&_pre]:overflow-x-auto [&_pre]:rounded [&_pre]:bg-surface-2 [&_pre]:p-3">
      <ReactMarkdown rehypePlugins={[rehypeSanitize]}>{children}</ReactMarkdown>
    </div>
  );
}
