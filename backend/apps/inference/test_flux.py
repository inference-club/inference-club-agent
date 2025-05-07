"""
Test script for Flux image generation.

To run this script:
1. Make sure your Django server is running (python manage.py runserver)
2. Run this script: python -m apps.inference.test_flux

The script will:
1. Send a request to generate an image
2. Print the request ID
3. Poll the status endpoint until the request is complete
4. Print the final status and response
"""

import requests
import json
import time
import logging
import random

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s - %(levelname)s - %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger(__name__)


def test_flux_image_generation():
    # API endpoint
    url = "http://localhost:8000/api/inference/"
    logger.info(f"Using API endpoint: {url}")
    seed = random.randint(1, 2147483647)

    # seed = 2198893635

    prompt = 'A small neon light sign that says "Inference Club" in bold large letters all caps. Theme is Generative AI with text images videos 3d models voice music, etc'
    prompt = 'a cartoon of two little girls in dresses walking to school. One of the girls says: "another day at school" comic strip in the style of li\'l abner 1940s printed cartoon'
    # Request payload
    payload = {
        "inference_type": "image_generation",
        "payload": {
            "prompt": prompt,
            "height": 1024,
            "width": 1024,
            "cfg_scale": 6,
            "mode": "base",
            "samples": 1,
            "seed": seed,
            "steps": 50,
        },
    }
    logger.info("Prepared request payload:")
    logger.info(json.dumps(payload, indent=2))

    try:
        # Make the initial request
        logger.info("Sending POST request to inference endpoint...")
        response = requests.post(
            url, json=payload, headers={"Content-Type": "application/json"}
        )
        logger.info(f"Received response with status code: {response.status_code}")

        # Print the response
        logger.info("Response content:")
        logger.info(json.dumps(response.json(), indent=2))

        # Get the request ID
        if "request_id" in response.json():
            request_id = response.json()["request_id"]
            logger.info(f"Request ID: {request_id}")
            status_url = f"{url}{request_id}/"
            logger.info(f"Status URL: {status_url}")

            # Poll the status endpoint until the request is complete
            while True:
                status_response = requests.get(status_url)
                status_data = status_response.json()
                current_status = status_data.get("status")

                logger.info(f"Current status: {current_status}")

                if current_status in ["completed", "failed"]:
                    logger.info("Final response:")
                    logger.info(json.dumps(status_data, indent=2))
                    break

                time.sleep(2)  # Wait 2 seconds before checking again

    except requests.exceptions.ConnectionError:
        logger.error("Failed to connect to the server. Is the Django server running?")
    except requests.exceptions.Timeout:
        logger.error("Request timed out. The server took too long to respond.")
    except requests.exceptions.RequestException as e:
        logger.error(f"An error occurred during the request: {str(e)}")
    except json.JSONDecodeError:
        logger.error("Failed to parse the response as JSON. Response content:")
        logger.error(response.text)
    except Exception as e:
        logger.error(f"An unexpected error occurred: {str(e)}")


if __name__ == "__main__":
    logger.info("Starting Flux image generation test...")
    test_flux_image_generation()
    logger.info("Test completed.")
