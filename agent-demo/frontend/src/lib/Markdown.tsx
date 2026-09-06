import { memo } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

function MarkdownImpl({ content }: { content: string }) {
  return (
    <div className="md-body">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{content || ""}</ReactMarkdown>
    </div>
  );
}

const Markdown = memo(MarkdownImpl);
export default Markdown;