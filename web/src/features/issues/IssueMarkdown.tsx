import { MarkdownBody } from '../attachments/MarkdownBody'
import styles from './IssueDiscussion.module.css'

export function IssueMarkdown({ body }: { body: string }) {
  return <MarkdownBody className={styles.markdown} value={body} />
}
