import { Card } from "./card";
import "./theme.css";

// One entry per slot you replace: `contract` is the slot contract major your view is written for.
export default { views: { "board.card": { contract: "^1", view: Card } } };
