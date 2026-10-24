import { TaskLink } from "@entire-vc/mesh-theme-sdk";
import type { BoardCardView } from "@entire-vc/mesh-theme-sdk";

// The red control: a board card that cannot be dragged breaks the board.card contract.
const Card: BoardCardView = ({ data }) => <TaskLink taskId={data.task.id}>{data.task.title}</TaskLink>;

export default { views: { "board.card": { contract: "^1", view: Card } } };
